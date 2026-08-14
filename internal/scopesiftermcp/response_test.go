package scopesiftermcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

func TestAutoUsesStableLeanShapeAcrossOriginalSizeBoundary(t *testing.T) {
	for _, budget := range []int{1024, 1536, 2048, 2560, 3072} {
		t.Run(fmt.Sprintf("budget-%d", budget), func(t *testing.T) {
			var previousKeys []string
			for _, originalBytes := range []int{budget - 1, budget, budget + 1} {
				response := findResponseWithSerializedBytes(t, originalBytes)
				result, output, sizing, err := prepareToolResponse(
					"find", responseAuto, response, budget,
				)
				if err != nil {
					t.Fatal(err)
				}
				lean, ok := output.(leanResponse)
				if !ok {
					t.Fatalf("auto output = %T, want leanResponse", output)
				}
				encoded := mustMarshalResponse(t, lean)
				if len(encoded) > budget || sizing.StructuredBytes != len(encoded) {
					t.Fatalf("lean bytes = %d, sizing %#v, budget %d", len(encoded), sizing, budget)
				}
				if got := sizing.Compacted; got != (originalBytes > budget) {
					t.Fatalf("original=%d compacted=%v", originalBytes, got)
				}
				keys := sortedTopLevelKeys(t, encoded)
				if previousKeys != nil && !reflect.DeepEqual(keys, previousKeys) {
					t.Fatalf("auto keys changed with size: %q then %q", previousKeys, keys)
				}
				previousKeys = keys
				assertBoundedNonJSONHint(t, result, encoded)
			}
		})
	}
}

func TestFullPreservesExactV3ShapeAndIgnoresBudget(t *testing.T) {
	response := navigator.FindResponse{
		NavigationBudget: &navigator.NavigationBudget{Used: 2, Limit: 20, Remaining: 18},
		Query:            "Target",
		Symbol:           "Target",
		MatchedAs:        navigator.FindOutcomeSymbol,
		Root:             "/repository",
		SearchedAs:       []navigator.FindMatch{navigator.FindMatchSymbol},
		Hint:             "legacy hint",
		Results:          largeResponseResults(),
		ResultsTruncated: true,
	}
	want := mustMarshalResponse(t, response)
	result, output, sizing, err := prepareToolResponse("find", responseFull, response, 0)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := output.(navigator.FindResponse)
	if !ok || !reflect.DeepEqual(actual, response) {
		t.Fatalf("full output = %T %#v", output, output)
	}
	if got := mustMarshalResponse(t, output); string(got) != string(want) {
		t.Fatalf("full JSON changed:\n%s\nwant:\n%s", got, want)
	}
	if sizing != (responseSizing{OriginalBytes: len(want), StructuredBytes: len(want)}) {
		t.Fatalf("full sizing = %#v", sizing)
	}
	assertBoundedNonJSONHint(t, result, want)
}

func TestLeanGoldenToolMappings(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	tests := []struct {
		name string
		tool string
		full any
		want string
	}{
		{
			name: "changed", tool: "changed",
			full: navigator.ChangedResponse{
				BaseCommit: base, HeadCommit: head, Patch: "+line\n",
				Results: []navigator.Result{{Path: "b.go", Line: 9, Kind: "changed", Finding: navigator.FindingOther}},
			},
			want: `{"target":"` + base + `..` + head + `","outcome":"changed","results":[{"location":"b.go:9","kind":"changed"}],"truncated":["patch"],"next":{"tool":"inspect","arguments":{"location":"b.go:9"}}}`,
		},
		{
			name: "find", tool: "find",
			full: navigator.FindResponse{
				Query: "Serve", MatchedAs: navigator.FindOutcomeSymbol,
				Results: []navigator.Result{{Path: "a.go", Line: 7, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Serve", Scope: "Serve", Signature: "func Serve()"}},
			},
			want: `{"target":"Serve","outcome":"symbol","results":[{"location":"a.go:7","kind":"def","symbol":"Serve","scope":"Serve","signature":"func Serve()"}],"truncated":[],"next":{"tool":"inspect","arguments":{"location":"a.go:7"}}}`,
		},
		{
			name: "inspect", tool: "inspect",
			full: navigator.InspectResponse{
				Location: "a.go:7", Symbol: "Serve",
				Results: []navigator.Result{{Path: "a.go", Line: 7, StartLine: 7, Kind: "scope", Finding: navigator.FindingOther, Symbol: "Serve", Code: "func Serve() {\n\twork()\n}"}},
			},
			want: `{"target":"a.go:7","outcome":"Serve","evidence":{"kind":"source","start":"a.go:7","text":"func Serve() {\n\twork()\n}"},"results":[],"truncated":[]}`,
		},
		{
			name: "outline", tool: "outline",
			full: navigator.OutlineResponse{
				Path:    "a.go",
				Results: []navigator.Result{{Path: "a.go", Line: 7, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Serve", Code: "func Serve()"}},
			},
			want: `{"target":"a.go","outcome":"definitions","results":[{"location":"a.go:7","kind":"def","symbol":"Serve"}],"truncated":["source"],"next":{"tool":"inspect","arguments":{"location":"a.go:7"}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, output, _, err := prepareToolResponse(test.tool, responseAuto, test.full, 1024)
			if err != nil {
				t.Fatal(err)
			}
			got := string(mustMarshalResponse(t, output))
			if got != test.want {
				t.Fatalf("lean golden changed:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestLeanResponseRemovesFormatterTelemetryAndDuplicateFinding(t *testing.T) {
	response := navigator.ChangedResponse{
		Root: "/secret/root", BaseCommit: "base", HeadCommit: "head",
		HeadSubject: "subject", Patch: strings.Repeat("patch", 300),
		Results: largeResponseResults(), PatchTruncated: true, ResultsTruncated: true,
		NavigationBudget: &navigator.NavigationBudget{Used: 3, Limit: 20, Remaining: 17},
	}
	_, output, _, err := prepareToolResponse("changed", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	encoded := mustMarshalResponse(t, output)
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &topLevel); err != nil {
		t.Fatal(err)
	}
	if _, exists := topLevel["tool"]; exists {
		t.Fatalf("lean response retained formatter tool field: %s", encoded)
	}
	for _, forbidden := range []string{
		`"root"`, `"searched_as"`, `"counts"`, `"omitted_bytes"`,
		`"original_bytes"`, `"budget_bytes"`, `"compact"`, `"full_response"`,
		`"metadata_truncated"`, `"finding"`, `"navigation_budget"`, "subject", "/secret/root",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("lean response retained %q: %s", forbidden, encoded)
		}
	}
}

func TestInspectEvidenceUsesOnlyCompleteUTF8LinesWithinBudget(t *testing.T) {
	lines := []string{
		"func Serve() {",
		"\tfirst := \"界界界\"",
		"\tsecond := \"" + strings.Repeat("界", 120) + "\"",
		"\tthird()",
		"}",
	}
	code := strings.Join(lines, "\n")
	response := navigator.InspectResponse{
		Location: "internal/service.go:40", Symbol: "Serve",
		Results: []navigator.Result{
			{Path: "internal/service.go", Line: 40, StartLine: 38, EndLine: 90, CodeStartLine: 38, CodeEndLine: 90, Kind: "scope", Finding: navigator.FindingOther, Symbol: "Serve", Code: code},
			{Path: "internal/service_test.go", Line: 12, Kind: "ref", Finding: navigator.FindingSymbol, Symbol: "Serve", Scope: "TestServe"},
		},
	}
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 420)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	encoded := mustMarshalResponse(t, lean)
	if len(encoded) > 420 || sizing.StructuredBytes != len(encoded) || !utf8.Valid(encoded) {
		t.Fatalf("lean evidence bytes = %d, sizing %#v", len(encoded), sizing)
	}
	if lean.Evidence == nil || lean.Evidence.Start != "internal/service.go:38" {
		t.Fatalf("evidence = %#v", lean.Evidence)
	}
	if lean.Evidence.Text == code || !strings.HasPrefix(code, lean.Evidence.Text+"\n") {
		t.Fatalf("evidence cut through line or was not bounded: %q", lean.Evidence.Text)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, lean.Evidence.Text) && line != lean.Evidence.Text {
			t.Fatalf("partial source line survived: %q", lean.Evidence.Text)
		}
	}
	if !containsString(lean.Truncated, "source") {
		t.Fatalf("source omission not exposed: %#v", lean.Truncated)
	}
	for _, result := range lean.Results {
		if result.Location == "internal/service.go:40" {
			t.Fatalf("primary inspect result duplicated evidence: %#v", lean.Results)
		}
	}
}

func TestInspectEvidenceDropsSingleOversizedLineWhole(t *testing.T) {
	line := "const payload = \"" + strings.Repeat("界", 600) + "\""
	response := navigator.InspectResponse{
		Location: "a.go:1", Symbol: "payload",
		Results: []navigator.Result{{Path: "a.go", Line: 1, StartLine: 1, Kind: "scope", Code: line}},
	}
	_, output, _, err := prepareToolResponse("inspect", responseAuto, response, 256)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") {
		t.Fatalf("oversized single line was split: %#v", lean)
	}
	if strings.Contains(string(mustMarshalResponse(t, lean)), "const payload") {
		t.Fatal("partial oversized line leaked")
	}
}

func TestLeanResultsDefinitionFirstWithExactlyOneExactNext(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{
			{Path: "caller.go", Line: 9, Kind: "ref", Finding: navigator.FindingSymbol, Symbol: "Target"},
			{Path: "target.go", Line: 3, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Target"},
			{Path: "other.go", Line: 12, Kind: "ref", Finding: navigator.FindingSymbol, Symbol: "Target"},
		},
	}
	result, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if len(lean.Results) != 3 || lean.Results[0].Location != "target.go:3" {
		t.Fatalf("definition-first results = %#v", lean.Results)
	}
	if lean.Next == nil || lean.Next.Tool != "inspect" ||
		lean.Next.Arguments["location"] != "target.go:3" {
		t.Fatalf("next = %#v", lean.Next)
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "Inspect target.go:3;") || len(text) > maximumCompactTextBytes {
		t.Fatalf("actionable text hint = %q", text)
	}
}

func TestOutlinePreservesDefinitionSourceOrder(t *testing.T) {
	response := navigator.OutlineResponse{
		Path: "a.go",
		Results: []navigator.Result{
			{Path: "a.go", Line: 20, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Later"},
			{Path: "a.go", Line: 4, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Earlier"},
		},
	}
	_, output, _, err := prepareToolResponse("outline", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	got := []string{lean.Results[0].Location, lean.Results[1].Location}
	want := []string{"a.go:20", "a.go:4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outline order = %q, want %q", got, want)
	}
}

func TestLeanNeverShortensExactLocationsOrActions(t *testing.T) {
	longPath := strings.Repeat("deep/", 80) + "file.go"
	location := longPath + ":42"
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{Path: longPath, Line: 42, Kind: "def", Finding: navigator.FindingSymbol}},
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if len(lean.Results) != 1 || lean.Results[0].Location != location ||
		lean.Next == nil || lean.Next.Arguments["location"] != location {
		t.Fatalf("location/action changed: %#v", lean)
	}

	tooLong := strings.Repeat("deep/", 300) + "file.go:42"
	inspect := navigator.InspectResponse{Location: tooLong, Symbol: "Target", Results: []navigator.Result{}}
	if _, _, _, err := prepareToolResponse("inspect", responseAuto, inspect, 1024); err == nil {
		t.Fatal("impossible exact inspect target was silently shortened")
	}
}

func TestLongQueryIsUTF8BoundedAndExposedAsTruncated(t *testing.T) {
	query := strings.Repeat("界\"\n", 1200)
	response := navigator.FindResponse{Query: query, MatchedAs: navigator.FindOutcomeNone, Results: []navigator.Result{}}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	encoded := mustMarshalResponse(t, lean)
	if len(encoded) > 1024 || !utf8.Valid(encoded) || lean.Target == query ||
		!containsString(lean.Truncated, "target") {
		t.Fatalf("bounded query = %#v (%d bytes)", lean, len(encoded))
	}
}

func TestActionHintFallsBackInsteadOfTruncatingLocation(t *testing.T) {
	location := strings.Repeat("deep/", 40) + "file.go:1"
	next := &leanNext{Tool: "inspect", Arguments: map[string]any{"location": location}}
	hint := leanNextHint(next)
	if hint != fullResponseTextHint || strings.Contains(hint, location[:20]) {
		t.Fatalf("long action hint was partially emitted: %q", hint)
	}
}

func TestPrepareToolResponseRejectsInvalidRequests(t *testing.T) {
	response := navigator.FindResponse{Query: "x", Results: []navigator.Result{}}
	if _, _, _, err := prepareToolResponse("find", "brief", response, 1024); err == nil {
		t.Fatal("unsupported response mode succeeded")
	}
	if _, _, _, err := prepareToolResponse("find", responseAuto, response, 0); err == nil {
		t.Fatal("zero auto budget succeeded")
	}
	if _, _, _, err := prepareToolResponse("changed", responseAuto, response, 1024); err == nil {
		t.Fatal("mismatched response type succeeded")
	}
	var nilFind *navigator.FindResponse
	if _, _, _, err := prepareToolResponse("find", responseAuto, nilFind, 1024); err == nil {
		t.Fatal("nil response succeeded")
	}
}

func largeResponseResults() []navigator.Result {
	results := []navigator.Result{
		compactTestResult("first.go", 7, navigator.FindingSymbol, "def", "Target", "Target", strings.Repeat("source ", 300)),
		compactTestResult("second.go", 11, navigator.FindingSymbol, "ref", "Target", "Caller", strings.Repeat("source ", 300)),
	}
	results[0].Signature = "func Target()"
	return results
}

func findResponseWithSerializedBytes(t *testing.T, target int) navigator.FindResponse {
	t.Helper()
	response := navigator.FindResponse{
		Query: "x", MatchedAs: navigator.FindOutcomeNone, Results: []navigator.Result{}, Hint: "x",
	}
	current := len(mustMarshalResponse(t, response))
	if current > target {
		t.Fatalf("minimum find response is %d bytes, exceeds target %d", current, target)
	}
	response.Hint = strings.Repeat("x", target-current+1)
	if got := len(mustMarshalResponse(t, response)); got != target {
		t.Fatalf("constructed find response = %d bytes, want %d", got, target)
	}
	return response
}

func compactTestResult(
	path string,
	line int,
	finding navigator.Finding,
	kind, symbol, scope, code string,
) navigator.Result {
	return navigator.Result{
		Path: path, Line: line, StartLine: line, EndLine: line,
		Finding: finding, Kind: kind, Symbol: symbol, Scope: scope, Code: code,
	}
}

func mustMarshalResponse(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func sortedTopLevelKeys(t *testing.T, encoded []byte) []string {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(decoded))
	for _, candidate := range []string{"target", "outcome", "evidence", "results", "truncated", "next", "error"} {
		if _, ok := decoded[candidate]; ok {
			keys = append(keys, candidate)
		}
	}
	if len(keys) != len(decoded) {
		t.Fatalf("unexpected top-level keys: %#v", decoded)
	}
	return keys
}

func assertBoundedNonJSONHint(t *testing.T, result *mcp.CallToolResult, structuredJSON []byte) {
	t.Helper()
	text := toolResultText(t, result)
	if text == "" || len(text) > maximumCompactTextBytes || !utf8.ValidString(text) {
		t.Fatalf("text hint = %q", text)
	}
	if text == string(structuredJSON) || strings.Contains(text, string(structuredJSON)) ||
		strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("text duplicated structured JSON: %q", text)
	}
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("content = %#v, want one text hint", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want TextContent", result.Content[0])
	}
	return text.Text
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}
