package codex

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	harnesscodex "github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness/codex"
)

func TestProviderResponseHeadersTraceUsesCanonicalOrderedValues(t *testing.T) {
	t.Parallel()
	header := http.Header{
		"Content-Type":         []string{"application/json; charset=utf-8"},
		"Openai-Model":         []string{"gpt-5.4-2026-03-05"},
		"X-Codex-Turn-State":   []string{"sticky"},
		"X-Reasoning-Included": []string{"true"},
	}
	trace, err := providerResponseHeadersTrace(header)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		got  string
		want []string
	}{
		{"content type", trace.ContentTypeSHA256, header.Values("Content-Type")},
		{"provider model", trace.ProviderModelSHA256, header.Values("Openai-Model")},
		{"turn state", trace.TurnStateSHA256, header.Values("X-Codex-Turn-State")},
		{"reasoning included", trace.ReasoningIncludedSHA256, header.Values("X-Reasoning-Included")},
	}
	for _, check := range checks {
		want, err := canonicalJSONDigest(check.want)
		if err != nil {
			t.Fatal(err)
		}
		if check.got != want {
			t.Fatalf("%s digest = %s, want %s", check.name, check.got, want)
		}
	}
	raw, mediaType, err := requiredResponseMediaType(header)
	if err != nil {
		t.Fatal(err)
	}
	if raw != "application/json; charset=utf-8" || mediaType != "application/json" {
		t.Fatalf("content type mapping = %q -> %q", raw, mediaType)
	}
}

func TestRequiredResponseMediaTypeRejectsAmbiguousOrUnsupportedValues(t *testing.T) {
	t.Parallel()
	for _, header := range []http.Header{
		{},
		{"Content-Type": []string{"application/json", "text/event-stream"}},
		{"Content-Type": []string{"text/plain"}},
		{"Content-Type": []string{"application/json; bad"}},
	} {
		if _, _, err := requiredResponseMediaType(header); err == nil {
			t.Fatalf("requiredResponseMediaType accepted %#v", header)
		}
	}
}

func TestValidateReviewedUpstreamRequestRejectsUnreviewedHeader(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-5.4"}`)
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.openai.com/v1/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", lifecycleVersion)
	want, err := providerRequestHeadersTrace(len(body), "", false, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewedUpstreamRequest(request, want, len(body)); err != nil {
		t.Fatalf("reviewed request rejected: %v", err)
	}
	request.Header.Set("X-Unreviewed-Behavior", "enabled")
	if err := validateReviewedUpstreamRequest(request, want, len(body)); err == nil {
		t.Fatal("unreviewed provider-visible request header was accepted")
	}
}

func TestCaptureAndCloseResponseBodyKeepsCloseFailureBeforeCompleted(t *testing.T) {
	t.Parallel()
	attempt := harnesscodex.ProviderResponseAttemptTrace{
		Stage:          harnesscodex.ResponseAttemptTransport,
		TLSConnections: []harnesscodex.TLSConnectionTrace{},
	}
	body := &closeErrorReadCloser{Reader: bytes.NewReader([]byte("complete body"))}
	content, err := captureAndCloseResponseBody(&attempt, body, 1024)
	if err == nil || !errors.Is(err, errTestResponseClose) {
		t.Fatalf("captureAndCloseResponseBody close error = %v", err)
	}
	if string(content) != "complete body" || !body.closed ||
		attempt.Stage != harnesscodex.ResponseAttemptResponseBody ||
		!attempt.BodyCaptured || !attempt.BodyComplete ||
		attempt.BodyBytes != len(content) || attempt.BodySHA256 != bytesDigest(content) {
		t.Fatalf("close-failed response attempt = %#v, body closed=%t", attempt, body.closed)
	}
}

func TestProductionTLSCollectorPreservesEvidenceWithError(t *testing.T) {
	t.Parallel()
	connection := harnesscodex.TLSConnectionTrace{
		DNSName:              productionUpstreamDNS,
		VerifiedChainsSHA256: [][]string{{strings.Repeat("a", 64)}},
		TLSVersion:           0x0304,
	}
	collectorError := errors.New("later TLS collection failure")
	collector := productionTLSCollector{
		err:         collectorError,
		connections: []harnesscodex.TLSConnectionTrace{connection},
	}
	connections, err := collector.result()
	if !errors.Is(err, collectorError) || len(connections) != 1 ||
		connections[0].DNSName != productionUpstreamDNS {
		t.Fatalf("collector result = %#v, %v", connections, err)
	}
	connections[0].VerifiedChainsSHA256[0][0] = strings.Repeat("b", 64)
	if collector.connections[0].VerifiedChainsSHA256[0][0] != strings.Repeat("a", 64) {
		t.Fatal("collector result aliased retained TLS evidence")
	}
}

var errTestResponseClose = errors.New("test response close failure")

type closeErrorReadCloser struct {
	*bytes.Reader
	closed bool
}

func (reader *closeErrorReadCloser) Close() error {
	reader.closed = true
	return errTestResponseClose
}
