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
	"strings"
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
) (resultErr error) {
	body, err := readBounded(request.Body, lifecycle.limits.maxRequestBytes)
	if err != nil {
		return fmt.Errorf("read Codex Responses request: %w", err)
	}
	if bytes.Contains(body, []byte(lifecycle.credential)) {
		return errors.New("codex child request contains the real upstream credential")
	}
	accept, acceptPresent, err := reviewedOptionalRequestHeader(request.Header, "Accept")
	if err != nil {
		return err
	}
	beta, betaPresent, err := reviewedOptionalRequestHeader(request.Header, "OpenAI-Beta")
	if err != nil {
		return err
	}
	sequence, requestTrace, declarations, turnState, err := session.captureRequest(
		body,
		request.Header[http.CanonicalHeaderKey(turnStateHeader)],
		accept,
		acceptPresent,
		beta,
		betaPresent,
	)
	if err != nil {
		return err
	}
	attempt := harnesscodex.ProviderResponseAttemptTrace{
		Sequence:       sequence,
		Stage:          harnesscodex.ResponseAttemptTransport,
		TLSConnections: []harnesscodex.TLSConnectionTrace{},
	}
	defer func() {
		if resultErr != nil && attempt.ErrorClass == "" {
			attempt.ErrorClass = attempt.Stage + "_failure"
		}
		if err := session.captureResponseAttempt(sequence, attempt); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()

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
	if acceptPresent {
		upstreamRequest.Header.Set("Accept", accept)
	}
	if betaPresent {
		upstreamRequest.Header["OpenAI-Beta"] = []string{beta}
	}
	if turnState != "" {
		upstreamRequest.Header[http.CanonicalHeaderKey(turnStateHeader)] = []string{turnState}
	}
	if err := validateReviewedUpstreamRequest(
		upstreamRequest,
		requestTrace.Headers,
		len(body),
	); err != nil {
		return err
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
	if tlsCollector != nil {
		var tlsErr error
		attempt.TLSConnections, tlsErr = tlsCollector.result()
		if attempt.TLSConnections == nil {
			attempt.TLSConnections = []harnesscodex.TLSConnectionTrace{}
		}
		err = errors.Join(err, tlsErr)
	}
	if err != nil {
		// net/http may return both a response and an error when CheckRedirect
		// rejects a provider redirect. Preserve that observed wire envelope and
		// close its body instead of silently classifying it as response-free
		// transport failure.
		if response != nil {
			attempt.StatusPresent = true
			attempt.StatusCode = response.StatusCode
			attempt.HeadersPresent = true
			var headerErr error
			attempt.Headers, headerErr = providerResponseHeadersTrace(response.Header)
			closeErr := response.Body.Close()
			if headerErr != nil || closeErr != nil {
				err = errors.Join(err, headerErr, closeErr)
			}
		}
		return fmt.Errorf("call upstream Responses endpoint: %w", err)
	}
	responseClosed := false
	defer func() {
		if !responseClosed {
			err := response.Body.Close()
			responseClosed = true
			if err == nil {
				return
			}
			resultErr = errors.Join(resultErr, fmt.Errorf("close upstream Responses body: %w", err))
		}
	}()
	attempt.StatusPresent = true
	attempt.StatusCode = response.StatusCode
	attempt.HeadersPresent = true
	attempt.Headers, err = providerResponseHeadersTrace(response.Header)
	if err != nil {
		attempt.Stage = harnesscodex.ResponseAttemptResponseHeaders
		return err
	}
	responseBody, err := captureAndCloseResponseBody(
		&attempt,
		response.Body,
		lifecycle.limits.maxResponseBytes,
	)
	responseClosed = true
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		attempt.Stage = harnesscodex.ResponseAttemptResponseStatus
		return fmt.Errorf("upstream Responses endpoint returned status %d", response.StatusCode)
	}
	attempt.Stage = harnesscodex.ResponseAttemptResponseHeaders
	if len(response.Header.Values("Content-Encoding")) != 0 {
		return errors.New("upstream Responses endpoint used unsupported content encoding")
	}
	rawContentType, responseMediaType, err := requiredResponseMediaType(response.Header)
	if err != nil {
		return err
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
		response.Header[http.CanonicalHeaderKey(turnStateHeader)],
	)
	if err != nil {
		return err
	}
	if bytes.Contains(responseBody, []byte(lifecycle.credential)) {
		return errors.New("upstream response reflected the real credential")
	}
	attempt.Stage = harnesscodex.ResponseAttemptSemanticValidation
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
	responseTrace.TLSConnections = cloneTLSConnectionTraces(attempt.TLSConnections)
	responseTrace.Wire.RawContentType = rawContentType
	if err := session.captureResponse(sequence, responseTrace); err != nil {
		return err
	}

	attempt.Stage = harnesscodex.ResponseAttemptDownstreamForward
	writer.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	writer.Header()[openAIModelHeader] = []string{providerModelHeader}
	writer.Header()[turnStateHeader] = []string{responseTurnState}
	if reasoningIncluded {
		writer.Header()[reasoningIncludedHeader] = []string{reasoningHeader}
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write(responseBody); err != nil {
		return fmt.Errorf("return sanitized upstream response to Codex child: %w", err)
	}
	attempt.Stage = harnesscodex.ResponseAttemptCompleted
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
		len(request.Header[http.CanonicalHeaderKey("X-API-Key")]) != 0 ||
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

// validateReviewedUpstreamRequest makes the sanitized semantic envelope
// complete for every application header that this proxy exposes upstream.
// HTTP framing (authority, content length, and HTTP/2 pseudo-headers) is bound
// separately by the pinned route, body length, method, and path.
func validateReviewedUpstreamRequest(
	request *http.Request,
	want harnesscodex.ProviderRequestHeadersTrace,
	bodyBytes int,
) error {
	if request == nil || request.Method != http.MethodPost || request.URL == nil ||
		request.URL.Path != responsesPath || request.URL.RawPath != "" ||
		request.URL.RawQuery != "" || request.URL.Fragment != "" ||
		request.ContentLength != int64(bodyBytes) {
		return errors.New("upstream request route or body length is outside the reviewed envelope")
	}
	allowed := map[string]struct{}{
		http.CanonicalHeaderKey("Authorization"): {},
		http.CanonicalHeaderKey("Content-Type"):  {},
		http.CanonicalHeaderKey("User-Agent"):    {},
		http.CanonicalHeaderKey("Accept"):        {},
		http.CanonicalHeaderKey("OpenAI-Beta"):   {},
		http.CanonicalHeaderKey(turnStateHeader): {},
	}
	for name := range request.Header {
		if _, ok := allowed[http.CanonicalHeaderKey(name)]; !ok {
			return fmt.Errorf("upstream request contains unreviewed application header %q", name)
		}
	}
	authorization := request.Header.Values("Authorization")
	contentType := request.Header.Values("Content-Type")
	userAgent := request.Header.Values("User-Agent")
	if len(authorization) != 1 || !strings.HasPrefix(authorization[0], "Bearer ") ||
		len(authorization[0]) == len("Bearer ") ||
		len(contentType) != 1 || contentType[0] != "application/json" ||
		len(userAgent) != 1 || userAgent[0] != lifecycleVersion {
		return errors.New("upstream request required headers are outside the reviewed envelope")
	}
	accept, acceptPresent, err := reviewedOptionalRequestHeader(request.Header, "Accept")
	if err != nil {
		return err
	}
	beta, betaPresent, err := reviewedOptionalRequestHeader(request.Header, "OpenAI-Beta")
	if err != nil {
		return err
	}
	turnState, turnStatePresent, err := reviewedOptionalRequestHeader(request.Header, turnStateHeader)
	if err != nil {
		return err
	}
	observed, err := providerRequestHeadersTrace(
		bodyBytes,
		accept,
		acceptPresent,
		beta,
		betaPresent,
		turnState,
	)
	if err != nil {
		return err
	}
	if turnStatePresent != (turnState != "") || !reflect.DeepEqual(observed, want) {
		return errors.New("upstream request differs from its reviewed semantic commitment")
	}
	return nil
}

func constantTimeAuthorizationEqual(observed, capability string) bool {
	observedDigest := sha256.Sum256([]byte(observed))
	expectedDigest := sha256.Sum256([]byte("Bearer " + capability))
	return subtle.ConstantTimeCompare(observedDigest[:], expectedDigest[:]) == 1
}

type productionTLSCollector struct {
	err         error
	connections []harnesscodex.TLSConnectionTrace
	mu          sync.Mutex
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
	result := make([]harnesscodex.TLSConnectionTrace, len(collector.connections))
	for index, connection := range collector.connections {
		result[index] = cloneTLSConnectionTrace(connection)
	}
	if len(result) == 0 {
		return result, errors.Join(
			collector.err,
			errors.New("production request omitted verified TLS connection evidence"),
		)
	}
	return result, collector.err
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

func cloneTLSConnectionTraces(
	source []harnesscodex.TLSConnectionTrace,
) []harnesscodex.TLSConnectionTrace {
	result := make([]harnesscodex.TLSConnectionTrace, len(source))
	for index, connection := range source {
		result[index] = cloneTLSConnectionTrace(connection)
	}
	return result
}

func (session *ArmSession) captureRequest(
	raw []byte,
	turnStateValues []string,
	accept string,
	acceptPresent bool,
	beta string,
	betaPresent bool,
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
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("responses request arrived outside the active arm")
	}
	if session.fatal != nil {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", session.fatal
	}
	if session.requestCount >= session.lifecycle.limits.maxRequests {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("codex arm exceeds its Responses request limit")
	}
	turnState := ""
	if session.requestCount == 0 {
		if len(turnStateValues) != 0 {
			return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("first Codex request unexpectedly contained turn state")
		}
	} else {
		if session.turnState == "" || len(turnStateValues) != 1 ||
			turnStateValues[0] != session.turnState || !validHeaderValue(turnStateValues[0]) {
			return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("codex request omitted or changed its sticky turn state")
		}
		turnState = session.turnState
	}
	headerTrace, err := providerRequestHeadersTrace(
		len(raw), accept, acceptPresent, beta, betaPresent, turnState,
	)
	if err != nil {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", err
	}
	trace.Headers = headerTrace
	toolsSHA256, err := canonicalJSONDigest(declarations)
	if err != nil {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", err
	}
	if session.trace.FirstRequest == nil {
		first := trace
		first.DynamicFields = cloneDynamicFields(trace.DynamicFields)
		session.trace.FirstRequest = &first
		session.declarations = append([]harnesscodex.ResponsesToolDeclaration(nil), declarations...)
	} else if !reflect.DeepEqual(session.declarations, declarations) {
		return 0, harnesscodex.ResponsesRequestTrace{}, nil, "", errors.New("codex provider tool declarations drifted within the arm")
	}
	sequence := session.requestCount
	session.requests = append(session.requests, harnesscodex.ResponsesRequestSnapshot{
		Sequence:                            sequence,
		Model:                               trace.Model,
		ExactBodySHA256:                     trace.ExactBodySHA256,
		NonceNormalizedNonToolPayloadSHA256: trace.NonceNormalizedNonToolPayloadSHA256,
		ToolsSHA256:                         toolsSHA256,
		Headers:                             trace.Headers,
		DynamicFields:                       cloneDynamicFields(trace.DynamicFields),
	})
	session.requestCount++
	requestTrace := trace
	requestTrace.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), trace.Tools...)
	requestTrace.DynamicFields = cloneDynamicFields(trace.DynamicFields)
	return sequence, requestTrace,
		append([]harnesscodex.ResponsesToolDeclaration(nil), declarations...), turnState, nil
}

func reviewedOptionalRequestHeader(
	header http.Header,
	name string,
) (string, bool, error) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 || !validHeaderValue(values[0]) {
		return "", false, fmt.Errorf("codex child sent an invalid %s header", name)
	}
	return values[0], true, nil
}

func providerRequestHeadersTrace(
	bodyBytes int,
	accept string,
	acceptPresent bool,
	beta string,
	betaPresent bool,
	turnState string,
) (harnesscodex.ProviderRequestHeadersTrace, error) {
	trace := harnesscodex.ProviderRequestHeadersTrace{
		ContentTypeSHA256:  bytesDigest([]byte("application/json")),
		UserAgentSHA256:    bytesDigest([]byte(lifecycleVersion)),
		AcceptPresent:      acceptPresent,
		OpenAIBetaPresent:  betaPresent,
		TurnStatePresent:   turnState != "",
		AuthorizationClass: harnesscodex.RequestAuthorizationBearerCredential,
	}
	if acceptPresent {
		trace.AcceptSHA256 = bytesDigest([]byte(accept))
	}
	if betaPresent {
		trace.OpenAIBetaSHA256 = bytesDigest([]byte(beta))
	}
	if trace.TurnStatePresent {
		trace.TurnStateSHA256 = bytesDigest([]byte(turnState))
	}
	exact := map[string]any{
		"method":              http.MethodPost,
		"path":                responsesPath,
		"body_bytes":          bodyBytes,
		"content_type_sha256": trace.ContentTypeSHA256,
		"user_agent_sha256":   trace.UserAgentSHA256,
		"accept_present":      trace.AcceptPresent,
		"accept_sha256":       trace.AcceptSHA256,
		"openai_beta_present": trace.OpenAIBetaPresent,
		"openai_beta_sha256":  trace.OpenAIBetaSHA256,
		"turn_state_present":  trace.TurnStatePresent,
		"turn_state_sha256":   trace.TurnStateSHA256,
		"authorization_class": trace.AuthorizationClass,
	}
	exactDigest, err := canonicalJSONDigest(exact)
	if err != nil {
		return harnesscodex.ProviderRequestHeadersTrace{}, err
	}
	trace.ReviewedSemanticSHA256 = exactDigest
	trace.ExactApplicationSHA256 = exactDigest
	exact["body_bytes"] = "committed-by-exact-request-body"
	if trace.TurnStatePresent {
		exact["turn_state_sha256"] = "reviewed-sticky-turn-state-nonce"
	}
	parityDigest, err := canonicalJSONDigest(exact)
	if err != nil {
		return harnesscodex.ProviderRequestHeadersTrace{}, err
	}
	trace.ParityProjectionSHA256 = parityDigest
	return trace, nil
}

func providerResponseHeadersTrace(
	header http.Header,
) (harnesscodex.ProviderResponseHeadersTrace, error) {
	type headerField struct {
		present *bool
		digest  *string
		name    string
	}
	trace := harnesscodex.ProviderResponseHeadersTrace{}
	fields := []headerField{
		{name: "Content-Type", present: &trace.ContentTypePresent, digest: &trace.ContentTypeSHA256},
		{name: "Content-Encoding", present: &trace.ContentEncodingPresent, digest: &trace.ContentEncodingSHA256},
		{name: openAIModelHeader, present: &trace.ProviderModelPresent, digest: &trace.ProviderModelSHA256},
		{name: turnStateHeader, present: &trace.TurnStatePresent, digest: &trace.TurnStateSHA256},
		{name: reasoningIncludedHeader, present: &trace.ReasoningIncludedPresent, digest: &trace.ReasoningIncludedSHA256},
	}
	manifest := make(map[string]any, len(fields))
	for _, field := range fields {
		values := append([]string(nil), header.Values(field.name)...)
		manifest[http.CanonicalHeaderKey(field.name)] = values
		if len(values) == 0 {
			continue
		}
		*field.present = true
		digest, err := canonicalJSONDigest(values)
		if err != nil {
			return harnesscodex.ProviderResponseHeadersTrace{}, err
		}
		*field.digest = digest
	}
	digest, err := canonicalJSONDigest(manifest)
	if err != nil {
		return harnesscodex.ProviderResponseHeadersTrace{}, err
	}
	trace.ReviewedSemanticSHA256 = digest
	return trace, nil
}

func requiredResponseMediaType(header http.Header) (string, string, error) {
	values := header.Values("Content-Type")
	if len(values) != 1 || !validHeaderValue(values[0]) {
		return "", "", errors.New(
			"upstream Responses endpoint omitted one valid content type",
		)
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" && mediaType != "text/event-stream" {
		return "", "", errors.New(
			"upstream Responses endpoint used an unsupported content type",
		)
	}
	return values[0], mediaType, nil
}

func (session *ArmSession) captureResponseHeaders(
	sequence int,
	turnStateValues []string,
) (string, string, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if sequence < 0 || sequence >= session.requestCount {
		return "", "", errors.New("responses response headers have an invalid sequence")
	}
	if len(turnStateValues) > 1 ||
		(len(turnStateValues) == 1 && !validHeaderValue(turnStateValues[0])) {
		return "", "", errors.New("upstream returned an invalid sticky turn state")
	}
	switch {
	case sequence == 0:
		if session.turnState != "" || len(turnStateValues) != 1 {
			return "", "", errors.New("first upstream response omitted a unique sticky turn state")
		}
		session.turnState = turnStateValues[0]
		turnStateSHA256, err := canonicalJSONDigest(turnStateValues)
		if err != nil {
			return "", "", err
		}
		session.turnStateSHA256 = turnStateSHA256
	case session.turnState == "":
		return "", "", errors.New("upstream response preceded sticky turn-state initialization")
	case len(turnStateValues) == 1 && turnStateValues[0] != session.turnState:
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
		return errors.New("responses response arrived outside the active arm")
	}
	if sequence < 0 || sequence >= session.requestCount {
		return errors.New("responses response has an invalid request sequence")
	}
	if _, duplicate := session.responses[sequence]; duplicate {
		return errors.New("duplicate Responses response sequence")
	}
	if session.lifecycle.production != (len(response.TLSConnections) != 0) {
		return errors.New("responses production TLS evidence presence is inconsistent")
	}
	session.responses[sequence] = response
	return nil
}

func (session *ArmSession) captureResponseAttempt(
	sequence int,
	attempt harnesscodex.ProviderResponseAttemptTrace,
) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionActive && session.state != sessionFinishing {
		return errors.New("provider response attempt arrived outside the active arm")
	}
	if sequence < 0 || sequence >= session.requestCount || attempt.Sequence != sequence {
		return errors.New("provider response attempt has an invalid request sequence")
	}
	if _, duplicate := session.responseAttempts[sequence]; duplicate {
		return errors.New("duplicate provider response attempt sequence")
	}
	session.responseAttempts[sequence] = attempt
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

func readBoundedPartial(reader io.Reader, limit int64) ([]byte, bool, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return content, false, err
	}
	if int64(len(content)) > limit {
		return content, false, errors.New("body exceeds configured byte limit")
	}
	return content, true, nil
}

func captureAndCloseResponseBody(
	attempt *harnesscodex.ProviderResponseAttemptTrace,
	body io.ReadCloser,
	limit int64,
) ([]byte, error) {
	attempt.Stage = harnesscodex.ResponseAttemptResponseBody
	content, complete, readErr := readBoundedPartial(body, limit)
	attempt.BodyCaptured = true
	attempt.BodyComplete = complete
	attempt.BodyBytes = len(content)
	attempt.BodySHA256 = bytesDigest(content)
	closeErr := body.Close()
	var result []error
	if readErr != nil {
		result = append(result, fmt.Errorf("read upstream Responses body: %w", readErr))
	}
	if closeErr != nil {
		result = append(result, fmt.Errorf("close upstream Responses body: %w", closeErr))
	}
	return content, errors.Join(result...)
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
