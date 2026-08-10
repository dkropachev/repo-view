package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"reflect"
	"sync"

	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
)

const (
	openAIModelHeader       = "OpenAI-Model"
	turnStateHeader         = "x-codex-turn-state"
	reasoningIncludedHeader = "x-reasoning-included"
)

func (lifecycle *Lifecycle) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	// This gate deliberately precedes all mutable lifecycle/session reads. An
	// unrelated process reaching the loopback port cannot join inflight state or
	// poison the active arm merely by sending malformed host traffic.
	if err := lifecycle.authenticateProxyRequest(request); err != nil {
		writeProxyError(writer, http.StatusUnauthorized)
		return
	}
	lifecycle.mu.Lock()
	session := lifecycle.active
	if lifecycle.closed || session == nil {
		lifecycle.mu.Unlock()
		writeProxyError(writer, http.StatusServiceUnavailable)
		return
	}
	session.inflight.Add(1)
	lifecycle.mu.Unlock()
	defer session.inflight.Done()

	if err := lifecycle.proxyRequest(request.Context(), writer, request, session); err != nil {
		session.recordFatal(err)
		writeProxyError(writer, http.StatusBadGateway)
	}
}

func (lifecycle *Lifecycle) proxyRequest(
	ctx context.Context,
	writer http.ResponseWriter,
	request *http.Request,
	session *ArmSession,
) error {
	body, err := readBounded(request.Body, lifecycle.limits.maxRequestBytes)
	if err != nil {
		return fmt.Errorf("read Codex Responses request: %w", err)
	}
	if bytes.Contains(body, []byte(lifecycle.credential)) {
		return errors.New("Codex child request contains the real upstream credential")
	}
	sequence, requestTrace, declarations, turnState, err := session.captureRequest(
		body,
		request.Header.Values(turnStateHeader),
	)
	if err != nil {
		return err
	}

	upstreamContext, cancel := context.WithTimeout(ctx, lifecycle.limits.upstreamTimeout)
	defer cancel()
	upstreamURL := *lifecycle.upstreamURL
	upstreamURL.Path = responsesPath
	upstreamRequest, err := http.NewRequestWithContext(
		upstreamContext,
		http.MethodPost,
		upstreamURL.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("construct upstream Responses request: %w", err)
	}
	upstreamRequest.Header.Set("Authorization", "Bearer "+lifecycle.credential)
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("User-Agent", lifecycleVersion)
	if accept := request.Header.Get("Accept"); accept != "" {
		upstreamRequest.Header.Set("Accept", accept)
	}
	if beta := request.Header.Get("OpenAI-Beta"); beta != "" {
		if !validHeaderValue(beta) {
			return errors.New("Codex child sent an invalid OpenAI-Beta header")
		}
		upstreamRequest.Header.Set("OpenAI-Beta", beta)
	}
	if turnState != "" {
		upstreamRequest.Header.Set(turnStateHeader, turnState)
	}
	var tlsCollector *productionTLSCollector
	if lifecycle.production {
		tlsCollector = &productionTLSCollector{}
		upstreamRequest = upstreamRequest.WithContext(httptrace.WithClientTrace(
			upstreamRequest.Context(),
			&httptrace.ClientTrace{GotConn: tlsCollector.gotConnection},
		))
	}

	response, err := lifecycle.client.Do(upstreamRequest)
	if err != nil {
		if tlsCollector != nil {
			_, tlsErr := tlsCollector.result()
			err = errors.Join(err, tlsErr)
		}
		return fmt.Errorf("call upstream Responses endpoint: %w", err)
	}
	defer response.Body.Close()
	var tlsConnections []harnesscodex.TLSConnectionTrace
	if tlsCollector != nil {
		tlsConnections, err = tlsCollector.result()
		if err != nil {
			return err
		}
	}
	if response.StatusCode != http.StatusOK {
		_, _ = readBounded(response.Body, lifecycle.limits.maxResponseBytes)
		return fmt.Errorf("upstream Responses endpoint returned status %d", response.StatusCode)
	}
	if response.Header.Get("Content-Encoding") != "" {
		return errors.New("upstream Responses endpoint used unsupported content encoding")
	}
	responseMediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return errors.New("upstream Responses endpoint omitted a valid content type")
	}
	providerModelHeader, err := requiredResponseHeader(
		response.Header,
		openAIModelHeader,
		session.expectedProvider,
	)
	if err != nil {
		return err
	}
	reasoningHeader, reasoningIncluded, err := optionalResponseHeader(
		response.Header,
		reasoningIncludedHeader,
	)
	if err != nil {
		return err
	}
	turnStateDigest, responseTurnState, err := session.captureResponseHeaders(
		sequence,
		response.Header.Values(turnStateHeader),
	)
	if err != nil {
		return err
	}
	responseBody, err := readBounded(response.Body, lifecycle.limits.maxResponseBytes)
	if err != nil {
		return fmt.Errorf("read upstream Responses body: %w", err)
	}
	if bytes.Contains(responseBody, []byte(lifecycle.credential)) {
		return errors.New("upstream response reflected the real credential")
	}
	responseTrace, err := parseResponsesBody(
		responseBody,
		responseMediaType,
		requestTrace,
		session.expectedProvider,
		declarations,
		lifecycle.limits.maxSSEEventBytes,
		lifecycle.limits.maxEvents,
	)
	if err != nil {
		return fmt.Errorf("capture upstream Responses body: %w", err)
	}
	responseTrace.ProviderModelHeader = providerModelHeader
	responseTrace.TurnStateSHA256 = turnStateDigest
	responseTrace.ReasoningIncluded = reasoningIncluded
	responseTrace.TLSConnections = tlsConnections
	if err := session.captureResponse(sequence, responseTrace); err != nil {
		return err
	}

	writer.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	writer.Header().Set(openAIModelHeader, providerModelHeader)
	writer.Header().Set(turnStateHeader, responseTurnState)
	if reasoningIncluded {
		writer.Header().Set(reasoningIncludedHeader, reasoningHeader)
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write(responseBody); err != nil {
		return fmt.Errorf("return sanitized upstream response to Codex child: %w", err)
	}
	return nil
}

func (lifecycle *Lifecycle) authenticateProxyRequest(request *http.Request) error {
	if lifecycle == nil || request == nil {
		return errors.New("local proxy request is unavailable")
	}
	proxyURL, err := url.Parse(lifecycle.layout.ProxyURL)
	if err != nil || request.Method != http.MethodPost ||
		request.Host != proxyURL.Host || request.URL.Scheme != "" ||
		request.URL.Host != "" || request.URL.Path != responsesPath ||
		request.URL.RawPath != "" || request.URL.RawQuery != "" ||
		request.URL.Fragment != "" {
		return errors.New("local proxy route authentication failed")
	}
	authorizations := request.Header.Values("Authorization")
	if len(authorizations) != 1 || !constantTimeAuthorizationEqual(
		authorizations[0],
		lifecycle.layout.LocalProxyCapability,
	) {
		return errors.New("local proxy capability authentication failed")
	}
	if request.Header.Get("Content-Encoding") != "" ||
		request.Header.Get("X-API-Key") != "" ||
		request.Header.Get("Api-Key") != "" ||
		request.Header.Get("Proxy-Authorization") != "" {
		return errors.New("local proxy header authentication failed")
	}
	requestMediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || requestMediaType != "application/json" {
		return errors.New("local proxy content type authentication failed")
	}
	if request.ContentLength > lifecycle.limits.maxRequestBytes {
		return errors.New("local proxy request length authentication failed")
	}
	return nil
}

func constantTimeAuthorizationEqual(observed, capability string) bool {
	observedDigest := sha256.Sum256([]byte(observed))
	expectedDigest := sha256.Sum256([]byte("Bearer " + capability))
	return subtle.ConstantTimeCompare(observedDigest[:], expectedDigest[:]) == 1
}

type productionTLSCollector struct {
	mu          sync.Mutex
	connections []harnesscodex.TLSConnectionTrace
	err         error
}

func (collector *productionTLSCollector) gotConnection(info httptrace.GotConnInfo) {
	connection, err := productionTLSConnectionTrace(info.Conn)
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.err != nil {
		return
	}
	if err != nil {
		collector.err = err
		return
	}
	if len(collector.connections) >= 16 {
		collector.err = errors.New("production request used too many TLS connections")
		return
	}
	collector.connections = append(collector.connections, connection)
}

func (collector *productionTLSCollector) result() (
	[]harnesscodex.TLSConnectionTrace,
	error,
) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.err != nil {
		return nil, collector.err
	}
	if len(collector.connections) == 0 {
		return nil, errors.New("production request omitted verified TLS connection evidence")
	}
	result := make([]harnesscodex.TLSConnectionTrace, len(collector.connections))
	for index, connection := range collector.connections {
		result[index] = cloneTLSConnectionTrace(connection)
	}
	return result, nil
}

func productionTLSConnectionTrace(connection net.Conn) (
	harnesscodex.TLSConnectionTrace,
	error,
) {
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		return harnesscodex.TLSConnectionTrace{}, errors.New(
			"production upstream connection is not TLS",
		)
	}
	return productionTLSStateTrace(tlsConnection.ConnectionState())
}

func productionTLSStateTrace(state tls.ConnectionState) (
	harnesscodex.TLSConnectionTrace,
	error,
) {
	if !state.HandshakeComplete || state.ServerName != productionUpstreamDNS ||
		state.Version < tls.VersionTLS12 || len(state.VerifiedChains) == 0 ||
		len(state.VerifiedChains) > 16 || len(state.PeerCertificates) == 0 {
		return harnesscodex.TLSConnectionTrace{}, errors.New(
			"production TLS state is outside the pinned verification policy",
		)
	}
	if err := state.PeerCertificates[0].VerifyHostname(productionUpstreamDNS); err != nil {
		return harnesscodex.TLSConnectionTrace{}, errors.New(
			"production TLS leaf does not verify the pinned DNS name",
		)
	}
	chains := make([][]string, len(state.VerifiedChains))
	for chainIndex, chain := range state.VerifiedChains {
		if len(chain) == 0 || len(chain) > 16 ||
			!bytes.Equal(chain[0].Raw, state.PeerCertificates[0].Raw) {
			return harnesscodex.TLSConnectionTrace{}, errors.New(
				"production TLS verified chain is invalid",
			)
		}
		chains[chainIndex] = make([]string, len(chain))
		for certificateIndex, certificate := range chain {
			if certificate == nil || len(certificate.Raw) == 0 {
				return harnesscodex.TLSConnectionTrace{}, errors.New(
					"production TLS chain contains an invalid certificate",
				)
			}
			fingerprint := sha256.Sum256(certificate.Raw)
			chains[chainIndex][certificateIndex] = hex.EncodeToString(fingerprint[:])
		}
	}
	return harnesscodex.TLSConnectionTrace{
		DNSName:              productionUpstreamDNS,
		VerifiedChainsSHA256: chains,
		TLSVersion:           state.Version,
	}, nil
}

func cloneTLSConnectionTrace(
	source harnesscodex.TLSConnectionTrace,
) harnesscodex.TLSConnectionTrace {
	clone := source
	clone.VerifiedChainsSHA256 = make([][]string, len(source.VerifiedChainsSHA256))
	for index, chain := range source.VerifiedChainsSHA256 {
		clone.VerifiedChainsSHA256[index] = append([]string(nil), chain...)
	}
	return clone
}

func (session *ArmSession) captureRequest(
	raw []byte,
	turnStateValues []string,
) (
	int,
	harnesscodex.ResponsesRequestTrace,
	[]harnesscodex.ResponsesToolDeclaration,
	string,
	error,
) {
	trace, declarations, err := parseResponsesRequest(raw, session.request)
	if err != nil {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionActive && session.state != sessionFinishing {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("Responses request arrived outside the active arm")
	}
	if session.fatal != nil {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", session.fatal
	}
	if session.requestCount >= session.lifecycle.limits.maxRequests {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("Codex arm exceeds its Responses request limit")
	}
	turnState := ""
	if session.requestCount == 0 {
		if len(turnStateValues) != 0 {
			return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("first Codex request unexpectedly contained turn state")
		}
	} else {
		if session.turnState == "" || len(turnStateValues) != 1 ||
			turnStateValues[0] != session.turnState || !validHeaderValue(turnStateValues[0]) {
			return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("Codex request omitted or changed its sticky turn state")
		}
		turnState = session.turnState
	}
	if session.trace.FirstRequest == nil {
		first := trace
		session.trace.FirstRequest = &first
		session.declarations = append([]harnesscodex.ResponsesToolDeclaration(nil), declarations...)
	} else if !reflect.DeepEqual(session.declarations, declarations) {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("Codex provider tool declarations drifted within the arm")
	}
	sequence := session.requestCount
	session.requestCount++
	requestTrace := trace
	requestTrace.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), trace.Tools...)
	return sequence, requestTrace,
		append([]harnesscodex.ResponsesToolDeclaration(nil), declarations...), turnState, nil
}

func (session *ArmSession) captureResponseHeaders(
	sequence int,
	turnStateValues []string,
) (string, string, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if sequence < 0 || sequence >= session.requestCount {
		return "", "", errors.New("Responses response headers have an invalid sequence")
	}
	if len(turnStateValues) > 1 ||
		(len(turnStateValues) == 1 && !validHeaderValue(turnStateValues[0])) {
		return "", "", errors.New("upstream returned an invalid sticky turn state")
	}
	if sequence == 0 {
		if session.turnState != "" || len(turnStateValues) != 1 {
			return "", "", errors.New("first upstream response omitted a unique sticky turn state")
		}
		session.turnState = turnStateValues[0]
		session.turnStateSHA256 = bytesDigest([]byte(session.turnState))
	} else if session.turnState == "" {
		return "", "", errors.New("upstream response preceded sticky turn-state initialization")
	} else if len(turnStateValues) == 1 && turnStateValues[0] != session.turnState {
		return "", "", errors.New("upstream changed its sticky turn state")
	}
	return session.turnStateSHA256, session.turnState, nil
}

func (session *ArmSession) captureResponse(
	sequence int,
	response harnesscodex.ResponsesResponseTrace,
) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionActive && session.state != sessionFinishing {
		return errors.New("Responses response arrived outside the active arm")
	}
	if sequence < 0 || sequence >= session.requestCount {
		return errors.New("Responses response has an invalid request sequence")
	}
	if _, duplicate := session.responses[sequence]; duplicate {
		return errors.New("duplicate Responses response sequence")
	}
	if session.lifecycle.production != (len(response.TLSConnections) != 0) {
		return errors.New("Responses production TLS evidence presence is inconsistent")
	}
	session.responses[sequence] = response
	return nil
}

func (session *ArmSession) recordFatal(err error) {
	if err == nil {
		return
	}
	session.mu.Lock()
	if session.fatal == nil {
		session.fatal = err
	}
	session.mu.Unlock()
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("body exceeds configured byte limit")
	}
	return content, nil
}

func validHeaderValue(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func requiredResponseHeader(header http.Header, name, expected string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || !validHeaderValue(values[0]) || values[0] != expected {
		return "", fmt.Errorf("upstream %s header differs from the pinned value", name)
	}
	return values[0], nil
}

func optionalResponseHeader(header http.Header, name string) (string, bool, error) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 || !validHeaderValue(values[0]) {
		return "", false, fmt.Errorf("upstream %s header is invalid", name)
	}
	// Codex v0.144.0 consumes x-reasoning-included with HeaderMap::contains_key:
	// presence is the complete behavioral value. Keep the bytes only so the
	// proxy can forward them unchanged; the sanitized trace commits the bool.
	return values[0], true, nil
}

func writeProxyError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, "tokenbench Codex proxy rejected the request\n")
}

var _ http.Handler = (*Lifecycle)(nil)
