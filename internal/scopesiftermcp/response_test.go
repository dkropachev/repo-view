package scopesiftermcp

import (
	"bytes"
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

func TestAutoFitsFixedOneKiBBudgetForEveryTool(t *testing.T) {
	results := append(largeResponseResults(), []navigator.Result{
		compactTestResult("third.go", 15, navigator.FindingSymbol, "ref", "Target", "Third", strings.Repeat("source ", 300)),
		compactTestResult("fourth.go", 19, navigator.FindingSymbol, "ref", "Target", "Fourth", strings.Repeat("source ", 300)),
	}...)
	tests := []struct {
		tool string
		full any
	}{
		{tool: "changed", full: navigator.ChangedResponse{
			BaseCommit: "base", HeadCommit: "head", Patch: strings.Repeat("+patch\n", 600), Results: results,
		}},
		{tool: "find", full: navigator.FindResponse{
			Query: "Target", MatchedAs: navigator.FindOutcomeSymbol, Results: results,
		}},
		{tool: "inspect", full: navigator.InspectResponse{
			Location: "first.go:7", Symbol: "Target", Results: append([]navigator.Result{{
				Path: "first.go", Line: 7, StartLine: 7, Kind: "scope", Code: strings.Repeat("line()\n", 300),
			}}, results...),
		}},
		{tool: "outline", full: navigator.OutlineResponse{Path: "first.go", Results: results}},
	}
	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			result, output, sizing, err := prepareToolResponse(test.tool, responseAuto, test.full, 1024)
			if err != nil {
				t.Fatal(err)
			}
			encoded := mustMarshalResponse(t, output)
			if len(encoded) > 1024 || sizing.StructuredBytes != len(encoded) {
				t.Fatalf("fixed-budget response = %d bytes, sizing %#v", len(encoded), sizing)
			}
			assertBoundedNonJSONHint(t, result, encoded)
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
			want: `{"target":"Serve","outcome":"symbol","selection":"unique","results":[{"location":"a.go:7","kind":"def","symbol":"Serve","scope":"Serve"}],"truncated":[],"next":{"tool":"inspect","arguments":{"location":"a.go:7"}}}`,
		},
		{
			name: "inspect", tool: "inspect",
			full: navigator.InspectResponse{
				Location: "a.go:7", Symbol: "Serve",
				Results: []navigator.Result{{Path: "a.go", Line: 7, StartLine: 7, Kind: "scope", Finding: navigator.FindingOther, Symbol: "Serve", Code: "func Serve() {\n\twork()\n}"}},
			},
			want: `{"target":"a.go:7","outcome":"Serve","evidence":{"kind":"scope","start":"a.go:7","text":"func Serve() {\n\twork()\n}"},"results":[],"truncated":[]}`,
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

func TestInspectOversizedScopeIsOmittedWhole(t *testing.T) {
	lines := []string{
		"func Serve() {",
		"\toversized := \"" + strings.Repeat("界", 120) + "\"",
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
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") ||
		len(lean.Results) == 0 || lean.Results[0].Location != "internal/service.go:40" ||
		lean.Results[0].Kind != "scope" {
		t.Fatalf("atomic evidence fallback = %#v", lean)
	}
	for _, line := range lines[:len(lines)-1] {
		if strings.Contains(string(encoded), line) {
			t.Fatalf("partial scope line survived: %q in %s", line, encoded)
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
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") ||
		len(lean.Results) != 1 || lean.Results[0].Location != "a.go:1" ||
		lean.Results[0].Kind != "scope" {
		t.Fatalf("oversized single line was split: %#v", lean)
	}
	if strings.Contains(string(mustMarshalResponse(t, lean)), "const payload") {
		t.Fatal("partial oversized line leaked")
	}
}

func TestInspectRetainsPrimaryScopeAndSkipsExactSelfFollowupWithoutSource(t *testing.T) {
	response := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve",
		Results: []navigator.Result{
			{Path: "a.go", Line: 7, Kind: "scope", Finding: navigator.FindingOther, Symbol: "Serve"},
			{Path: "a.go", Line: 7, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Serve"},
			{Path: "caller.go", Line: 19, Kind: "ref", Finding: navigator.FindingSymbol, Symbol: "Serve"},
		},
	}
	result, output, _, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if len(lean.Results) != 2 || lean.Results[0].Location != "a.go:7" ||
		lean.Results[0].Kind != "scope" || lean.Results[1].Location != "caller.go:19" ||
		!containsString(lean.Truncated, "source") {
		t.Fatalf("inspect results retained self action: %#v", lean.Results)
	}
	if lean.Next == nil || lean.Next.Arguments["location"] != "caller.go:19" {
		t.Fatalf("inspect next = %#v", lean.Next)
	}
	if hint := toolResultText(t, result); hint !=
		"Inspect caller.go:19; details in structuredContent." {
		t.Fatalf("inspect hint = %q", hint)
	}
}

func TestInspectEvidenceWinsTightBudgetOverFollowupMetadata(t *testing.T) {
	response := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve",
		Results: []navigator.Result{
			{Path: "a.go", Line: 7, StartLine: 7, Kind: "scope", Code: "func Serve() {\n\twork()\n}"},
			{Path: strings.Repeat("deep/", 24) + "caller.go", Line: 19, Kind: "ref", Symbol: "Serve"},
		},
	}
	result, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 220)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence == nil || lean.Evidence.Text != response.Results[0].Code {
		t.Fatalf("direct evidence lost to follow-up metadata: %#v", lean)
	}
	if lean.Next != nil || len(lean.Results) != 0 || sizing.StructuredBytes > 220 {
		t.Fatalf("tight response = %#v, sizing %#v", lean, sizing)
	}
	if hint := toolResultText(t, result); hint !=
		"Source a.go:7; details in structuredContent." {
		t.Fatalf("evidence hint = %q", hint)
	}
}

func TestInspectEvidenceIsAtomicAtExactSerializedBoundary(t *testing.T) {
	code := "func Serve() {\n\tprintln(\"界🚀\")\n\twork()\n}"
	response := navigator.InspectResponse{
		Location: "internal/service.go:9", Symbol: "Serve",
		Results: []navigator.Result{{
			Path: "internal/service.go", Line: 9, StartLine: 7, EndLine: 10,
			CodeStartLine: 7, CodeEndLine: 10, Kind: "scope", Symbol: "Serve", Code: code,
		}},
	}
	_, completeOutput, _, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	complete := completeOutput.(leanResponse)
	if complete.Evidence == nil || complete.Evidence.Kind != "scope" ||
		complete.Evidence.Start != "internal/service.go:7" ||
		complete.Evidence.Text != code {
		t.Fatalf("complete evidence = %#v", complete)
	}
	boundary := len(mustMarshalResponse(t, complete))

	_, exactOutput, exactSizing, err := prepareToolResponse(
		"inspect", responseAuto, response, boundary,
	)
	if err != nil {
		t.Fatal(err)
	}
	exact := exactOutput.(leanResponse)
	if exact.Evidence == nil || exact.Evidence.Text != code ||
		exactSizing.StructuredBytes != boundary {
		t.Fatalf("exact boundary = %#v, sizing %#v", exact, exactSizing)
	}

	_, fallbackOutput, fallbackSizing, err := prepareToolResponse(
		"inspect", responseAuto, response, boundary-1,
	)
	if err != nil {
		t.Fatal(err)
	}
	fallback := fallbackOutput.(leanResponse)
	if fallback.Evidence != nil || !containsString(fallback.Truncated, "source") ||
		len(fallback.Results) != 1 ||
		fallback.Results[0].Location != "internal/service.go:9" ||
		fallback.Results[0].Kind != "scope" || fallbackSizing.StructuredBytes > boundary-1 {
		t.Fatalf("below boundary = %#v, sizing %#v", fallback, fallbackSizing)
	}
	if bytes.Contains(mustMarshalResponse(t, fallback), []byte("println")) {
		t.Fatalf("below-boundary fallback leaked source: %#v", fallback)
	}
}

func TestInspectEveryBudgetReturnsCompleteScopeOrExactFallback(t *testing.T) {
	code := "func Serve() {\n" + strings.Repeat("\twork(\"界🚀\")\n", 40) + "}"
	response := navigator.InspectResponse{
		Location: "deep/service.go:7", Symbol: "Serve",
		Results: []navigator.Result{{
			Path: "deep/service.go", Line: 7, StartLine: 7, EndLine: 48,
			CodeStartLine: 7, CodeEndLine: 48, Kind: "scope", Symbol: "Serve", Code: code,
		}},
	}
	firstSuccess := 0
	for budget := 1; budget <= 1024; budget++ {
		_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, budget)
		if err != nil {
			if firstSuccess != 0 {
				t.Fatalf("budget %d failed after success at %d: %v", budget, firstSuccess, err)
			}
			continue
		}
		if firstSuccess == 0 {
			firstSuccess = budget
		}
		lean := output.(leanResponse)
		encoded := mustMarshalResponse(t, lean)
		if len(encoded) > budget || sizing.StructuredBytes != len(encoded) || !utf8.Valid(encoded) {
			t.Fatalf("budget %d sizing = %d, %#v", budget, len(encoded), sizing)
		}
		if lean.Evidence != nil {
			if lean.Evidence.Kind != "scope" || lean.Evidence.Start != "deep/service.go:7" ||
				lean.Evidence.Text != code || len(lean.Results) != 0 {
				t.Fatalf("budget %d incomplete evidence = %#v", budget, lean)
			}
			continue
		}
		if !containsString(lean.Truncated, "source") || len(lean.Results) != 1 ||
			lean.Results[0].Location != "deep/service.go:7" || lean.Results[0].Kind != "scope" ||
			bytes.Contains(encoded, []byte("work(")) {
			t.Fatalf("budget %d invalid fallback = %#v", budget, lean)
		}
	}
	if firstSuccess <= 1 {
		t.Fatalf("first successful fallback budget = %d", firstSuccess)
	}
}

func TestInspectNavigatorTruncationNeverBecomesEvidence(t *testing.T) {
	partial := "func Serve() {\n\twork()"
	response := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve",
		Results: []navigator.Result{{
			Path: "a.go", Line: 7, StartLine: 7, EndLine: 100,
			CodeStartLine: 7, CodeEndLine: 8, Kind: "scope", Symbol: "Serve",
			Code: partial, CodeTruncated: true,
		}},
	}
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	encoded := mustMarshalResponse(t, lean)
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") ||
		len(lean.Results) != 1 || lean.Results[0].Location != "a.go:7" ||
		lean.Results[0].Kind != "scope" || bytes.Contains(encoded, []byte(partial)) ||
		sizing.StructuredBytes > 1024 {
		t.Fatalf("navigator-truncated evidence = %#v, sizing %#v", lean, sizing)
	}
}

func TestInspectInvalidUTF8NeverBecomesEvidence(t *testing.T) {
	invalid := string([]byte{'f', 'u', 'n', 'c', ' ', 0xff, '(', ')'})
	response := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve",
		Results: []navigator.Result{{
			Path: "a.go", Line: 7, StartLine: 7, EndLine: 7,
			CodeStartLine: 7, CodeEndLine: 7, Kind: "scope", Symbol: "Serve", Code: invalid,
		}},
	}
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	encoded := mustMarshalResponse(t, lean)
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") ||
		len(lean.Results) != 1 || lean.Results[0].Location != "a.go:7" ||
		lean.Results[0].Kind != "scope" || bytes.Contains(encoded, []byte("func")) ||
		!utf8.Valid(encoded) || sizing.StructuredBytes > 1024 {
		t.Fatalf("invalid UTF-8 evidence = %#v, sizing %#v", lean, sizing)
	}
}

func TestInspectLateEvidenceEvictionAddsFallbackOnce(t *testing.T) {
	code := "func Serve() {\n\twork()\n}"
	base := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve",
		Results: []navigator.Result{{
			Path: "a.go", Line: 7, StartLine: 7, EndLine: 9,
			CodeStartLine: 7, CodeEndLine: 9, Kind: "scope", Symbol: "Serve", Code: code,
		}},
	}
	_, completeOutput, _, err := prepareToolResponse("inspect", responseAuto, base, 1024)
	if err != nil {
		t.Fatal(err)
	}
	budget := len(mustMarshalResponse(t, completeOutput))
	response := base
	response.Results = append(response.Results, navigator.Result{
		Path: strings.Repeat("deep/", 80) + "caller.go", Line: 19,
		Kind: "ref", Symbol: "Serve",
	})
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, budget)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || len(lean.Results) != 1 ||
		lean.Results[0].Location != "a.go:7" || lean.Results[0].Kind != "scope" ||
		!containsString(lean.Truncated, "source") ||
		!containsString(lean.Truncated, "results") || sizing.StructuredBytes > budget {
		t.Fatalf("late evidence fallback = %#v, sizing %#v", lean, sizing)
	}
}

func TestInspectFallbackCountsTowardResultLimit(t *testing.T) {
	results := []navigator.Result{{
		Path: "a.go", Line: 7, StartLine: 7, Kind: "scope", Symbol: "Serve",
	}}
	for index := range maximumLeanResults + 2 {
		results = append(results, navigator.Result{
			Path: fmt.Sprintf("caller%d.go", index), Line: index + 1,
			Kind: "ref", Symbol: "Serve",
		})
	}
	response := navigator.InspectResponse{
		Location: "a.go:7", Symbol: "Serve", Results: results,
	}
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || len(lean.Results) != maximumLeanResults ||
		lean.Results[0].Location != "a.go:7" || lean.Results[0].Kind != "scope" ||
		!containsString(lean.Truncated, "source") ||
		!containsString(lean.Truncated, "results") || sizing.StructuredBytes > 1024 {
		t.Fatalf("fallback result cap = %#v, sizing %#v", lean, sizing)
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
	if lean.Selection != selectionUnique || len(lean.Results) != 3 ||
		lean.Results[0].Location != "target.go:3" {
		t.Fatalf("definition-first results = %#v", lean.Results)
	}
	if lean.Next == nil || lean.Next.Tool != "inspect" ||
		lean.Next.Arguments["location"] != "target.go:3" {
		t.Fatalf("next = %#v", lean.Next)
	}
	text := toolResultText(t, result)
	if text != "Inspect target.go:3; details in structuredContent." ||
		len(text) > maximumCompactTextBytes {
		t.Fatalf("actionable text hint = %q", text)
	}
}

func TestFindResponseNeverEmbedsSource(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "target.go", Line: 3, StartLine: 3, Kind: "def",
			Finding: navigator.FindingSymbol, Symbol: "Target", Scope: "Target",
			Signature:     "func Target()",
			CodeStartLine: 3, CodeEndLine: 5,
			Code: "func Target() {\n\twork()\n}",
		}},
	}
	result, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || len(lean.Results) != 1 ||
		lean.Results[0].Location != "target.go:3" || lean.Results[0].Signature != "" {
		t.Fatalf("find index = %#v", lean)
	}
	if lean.Next == nil || lean.Next.Arguments["location"] != "target.go:3" ||
		!containsString(lean.Truncated, "source") || sizing.StructuredBytes > 1024 {
		t.Fatalf("find source omission = %#v, sizing %#v", lean, sizing)
	}
	if text := toolResultText(t, result); text != "Inspect target.go:3; details in structuredContent." {
		t.Fatalf("find index hint = %q", text)
	}
}

func TestFindAmbiguousDefinitionsDoNotInlineArbitrarySource(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{
			{Path: "a.go", Line: 3, Kind: "def", Symbol: "Target", CodeStartLine: 3, Code: "func Target() {}"},
			{Path: "b.go", Line: 7, Kind: "def", Symbol: "Target", CodeStartLine: 7, Code: "func Target() {}"},
		},
	}
	result, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || lean.Selection != selectionAmbiguous || lean.Next != nil ||
		lean.Related != nil || len(lean.Results) != 2 ||
		lean.Results[0].Location != "a.go:3" || lean.Results[1].Location != "b.go:7" {
		t.Fatalf("ambiguous find = %#v", lean)
	}
	if hint := toolResultText(t, result); hint != structuredContentTextHint {
		t.Fatalf("ambiguous find hint = %q", hint)
	}
	fullResult, _, _, err := prepareToolResponse("find", responseFull, response, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hint := toolResultText(t, fullResult); hint != structuredContentTextHint {
		t.Fatalf("ambiguous full hint = %q", hint)
	}
}

func TestFindSelectionStates(t *testing.T) {
	definition := func(path string, line int) navigator.Result {
		return navigator.Result{Path: path, Line: line, Kind: "def", Symbol: "Target"}
	}
	reference := navigator.Result{Path: "caller.go", Line: 9, Kind: "ref", Symbol: "Target"}
	tests := []struct {
		name      string
		outcome   navigator.FindOutcome
		results   []navigator.Result
		truncated bool
		selection string
		allowNext bool
	}{
		{name: "unique", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{definition("a.go", 3)}, selection: selectionUnique, allowNext: true},
		{name: "ambiguous", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{definition("a.go", 3), definition("b.go", 7)}, selection: selectionAmbiguous},
		{name: "truncated", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{definition("a.go", 3)}, truncated: true, selection: selectionIncomplete},
		{name: "known ambiguity wins over truncation", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{definition("a.go", 3), definition("b.go", 7)}, truncated: true, selection: selectionAmbiguous},
		{name: "references only", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{reference}, selection: selectionIncomplete},
		{name: "duplicate location", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{definition("a.go", 3), definition("a.go", 3)}, selection: selectionUnique, allowNext: true},
		{name: "unactionable definition", outcome: navigator.FindOutcomeSymbol, results: []navigator.Result{{Path: "a.go", Kind: "def", Symbol: "Target"}}, selection: selectionIncomplete},
		{name: "file outcome", outcome: navigator.FindOutcomeFile, results: []navigator.Result{{Path: "a.go", Kind: "file"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := navigator.FindResponse{
				Query: "Target", MatchedAs: test.outcome, Results: test.results,
				ResultsTruncated: test.truncated,
			}
			result, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
			if err != nil {
				t.Fatal(err)
			}
			lean := output.(leanResponse)
			if lean.Selection != test.selection || (lean.Next != nil) != test.allowNext ||
				lean.Related != nil || sizing.StructuredBytes > 1024 {
				t.Fatalf("selection response = %#v, sizing %#v", lean, sizing)
			}
			if !test.allowNext && test.outcome != navigator.FindOutcomeNone &&
				toolResultText(t, result) != structuredContentTextHint {
				t.Fatalf("non-unique hint = %q", toolResultText(t, result))
			}
			fullResult, _, _, err := prepareToolResponse("find", responseFull, response, 0)
			if err != nil {
				t.Fatal(err)
			}
			fullHint := toolResultText(t, fullResult)
			if test.allowNext && !strings.HasPrefix(fullHint, "Inspect ") {
				t.Fatalf("unique full hint = %q", fullHint)
			}
			if !test.allowNext && test.outcome != navigator.FindOutcomeNone &&
				fullHint != structuredContentTextHint {
				t.Fatalf("non-unique full hint = %q", fullHint)
			}
		})
	}
}

func TestFindMandatoryPrimaryCandidateAtEveryBudgetBoundary(t *testing.T) {
	tests := []struct {
		name      string
		response  navigator.FindResponse
		location  string
		selection string
	}{
		{
			name: "unique",
			response: navigator.FindResponse{
				Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
				Results: []navigator.Result{{
					Path: strings.Repeat("deep/", 30) + "target.go", Line: 42,
					Kind: "def", Symbol: strings.Repeat("Target", 30),
				}},
			},
			location: strings.Repeat("deep/", 30) + "target.go:42", selection: selectionUnique,
		},
		{
			name: "incomplete",
			response: navigator.FindResponse{
				Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
				Results: []navigator.Result{{
					Path: strings.Repeat("nested/", 25) + "target.go", Line: 17,
					Kind: "def", Symbol: "Target",
				}},
				ResultsTruncated: true,
			},
			location: strings.Repeat("nested/", 25) + "target.go:17", selection: selectionIncomplete,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstSuccess := 0
			for budget := 1; budget <= 1024; budget++ {
				_, output, sizing, err := prepareToolResponse("find", responseAuto, test.response, budget)
				if err != nil {
					if firstSuccess != 0 {
						t.Fatalf("budget %d failed after first success at %d: %v", budget, firstSuccess, err)
					}
					continue
				}
				if firstSuccess == 0 {
					firstSuccess = budget
				}
				lean := output.(leanResponse)
				encoded := mustMarshalResponse(t, lean)
				if lean.Selection != test.selection || len(lean.Results) == 0 ||
					lean.Results[0].Location != test.location || lean.Results[0].Kind != "def" ||
					len(encoded) > budget || sizing.StructuredBytes != len(encoded) {
					t.Fatalf("budget %d response = %#v, sizing %#v", budget, lean, sizing)
				}
			}
			if firstSuccess <= 1 {
				t.Fatalf("first successful mandatory response budget = %d", firstSuccess)
			}
			if _, _, _, err := prepareToolResponse(
				"find", responseAuto, test.response, firstSuccess-1,
			); err == nil {
				t.Fatalf("budget %d succeeded without room for mandatory row", firstSuccess-1)
			}
		})
	}
}

func TestFindMandatoryCandidateSurvivesOneKiBPressure(t *testing.T) {
	primaryPath := strings.Repeat("package/", 35) + "image.py"
	results := []navigator.Result{{
		Path: primaryPath, Line: 307, Kind: "def", Symbol: "_make_image",
		Scope: strings.Repeat("scope", 80), Code: strings.Repeat("source line\n", 500),
	}}
	for index := range 20 {
		results = append(results, navigator.Result{
			Path: fmt.Sprintf("%s/caller-%02d.py", strings.Repeat("related/", 12), index),
			Line: index + 1, Kind: "ref", Symbol: "_make_image",
			Scope: strings.Repeat("caller", 40), Code: strings.Repeat("other source\n", 100),
		})
	}
	response := navigator.FindResponse{
		Query: strings.Repeat("_make_image", 30), MatchedAs: navigator.FindOutcomeSymbol,
		Results: results, ResultsTruncated: true,
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Selection != selectionIncomplete || lean.Evidence != nil || lean.Next != nil ||
		len(lean.Results) == 0 || lean.Results[0].Location != primaryPath+":307" ||
		lean.Results[0].Kind != "def" || !containsString(lean.Truncated, "results") ||
		!containsString(lean.Truncated, "source") || sizing.StructuredBytes > 1024 {
		t.Fatalf("pressured find = %#v, sizing %#v", lean, sizing)
	}
}

func TestFindIndexOmitsCompleteUTF8Source(t *testing.T) {
	code := "func 函数() {\n\tprintln(\"界🚀\")\n\tprintln(\"second complete line\")\n\tprintln(\"third complete line\")\n}"
	response := navigator.FindResponse{
		Query: "函数", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "unicode.go", Line: 3, Kind: "def", Symbol: "函数",
			CodeStartLine: 3, CodeEndLine: 7, Code: code,
		}},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 320)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	encoded := mustMarshalResponse(t, lean)
	if sizing.StructuredBytes > 320 || !utf8.Valid(encoded) || lean.Evidence != nil ||
		lean.Next == nil || lean.Next.Arguments["location"] != "unicode.go:3" ||
		!containsString(lean.Truncated, "source") || bytes.Contains(encoded, []byte(code)) {
		t.Fatalf("tight UTF-8 index = %#v (%d bytes)", lean, sizing.StructuredBytes)
	}
}

func TestFindOversizedSingleLineKeepsInspectFallback(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "target.go", Line: 3, Kind: "def", Symbol: "Target",
			CodeStartLine: 3, CodeEndLine: 3, Code: strings.Repeat("界", 300),
		}},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 320)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if sizing.StructuredBytes > 320 || lean.Evidence != nil || lean.Next == nil ||
		lean.Next.Arguments["location"] != "target.go:3" ||
		!containsString(lean.Truncated, "source") {
		t.Fatalf("oversized-line fallback = %#v, sizing %#v", lean, sizing)
	}
}

func TestFindIndexOmitsLongPreludeSource(t *testing.T) {
	prelude := strings.Repeat("// long decorator and prelude line\n", 80)
	code := prelude + "func Target() {\n\twork()\n}"
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "target.go", Line: 81, StartLine: 1, Kind: "def", Symbol: "Target",
			CodeStartLine: 1, CodeEndLine: 83, Code: code,
		}},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 320)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if sizing.StructuredBytes > 320 || lean.Evidence != nil ||
		!containsString(lean.Truncated, "source") || lean.Next == nil ||
		lean.Next.Arguments["location"] != "target.go:81" {
		t.Fatalf("long-prelude index = %#v, sizing %#v", lean, sizing)
	}
}

func TestFindNavigatorTruncatedEvidenceRetainsExactInspect(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "target.go", Line: 3, Kind: "def", Symbol: "Target",
			CodeStartLine: 3, CodeEndLine: 4, Code: "func Target() {\n\twork()",
			CodeTruncated: true,
		}},
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || !containsString(lean.Truncated, "source") || lean.Next == nil ||
		lean.Next.Arguments["location"] != "target.go:3" {
		t.Fatalf("navigator-truncated index = %#v", lean)
	}
}

func TestFindIndexFitsItsExactSerializedBoundary(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{{
			Path: "target.go", Line: 3, Kind: "def", Symbol: "Target",
			CodeStartLine: 3, CodeEndLine: 5,
			Code: "func Target() {\n\twork()\n}",
		}},
	}
	_, initial, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	index := initial.(leanResponse)
	budget := len(mustMarshalResponse(t, index))

	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, budget)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if sizing.StructuredBytes != budget || lean.Evidence != nil || lean.Next == nil ||
		lean.Next.Arguments["location"] != "target.go:3" ||
		!containsString(lean.Truncated, "source") {
		t.Fatalf("boundary response = %#v, sizing %#v", lean, sizing)
	}
}

func TestFindKeepsDefinitionBeforeDiverseReferences(t *testing.T) {
	results := []navigator.Result{{
		Path: "target.go", Line: 3, Kind: "def", Symbol: "Target", Scope: "Target",
	}}
	for index := range 6 {
		results = append(results, navigator.Result{
			Path: fmt.Sprintf("caller%d.go", index), Line: index + 5,
			Kind: "ref", Symbol: "Target", Scope: fmt.Sprintf("Caller%d", index),
		})
	}
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol, Results: results,
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Evidence != nil || lean.Next == nil || len(lean.Results) != maximumLeanResults ||
		lean.Results[0].Location != "target.go:3" ||
		!containsString(lean.Truncated, "results") || sizing.StructuredBytes > 1024 {
		t.Fatalf("definition/reference index = %#v, sizing %#v", lean, sizing)
	}
	seen := map[string]bool{}
	for _, result := range lean.Results {
		path := strings.SplitN(result.Location, ":", 2)[0]
		if seen[path] {
			t.Fatalf("duplicate file in diverse results: %#v", lean.Results)
		}
		seen[path] = true
	}
}

func TestFindPathDoesNotPrivilegeFollowUp(t *testing.T) {
	response := navigator.FindResponse{
		Query: "validator", MatchedAs: navigator.FindOutcomeFile,
		Results: []navigator.Result{
			{
				Path: "cmd/validator/main.go", Line: 42, Kind: "changed",
				Finding: navigator.FindingFile, Scope: "selectSymbols",
			},
			{Path: "cmd/validator/main_test.go", Kind: "file", Finding: navigator.FindingFile},
		},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Selection != "" || lean.Next != nil || lean.Related != nil ||
		sizing.StructuredBytes > 1024 {
		t.Fatalf("path response = %#v, sizing %#v", lean, sizing)
	}
}

func TestFindRelatedActionRejectsUnrelatedPaths(t *testing.T) {
	for _, candidate := range []string{"README.md", "cmd/validator/other_test.go", "cmd/validator/main.go"} {
		response := navigator.FindResponse{
			Query: "validator", MatchedAs: navigator.FindOutcomeFile,
			Results: []navigator.Result{
				{Path: "cmd/validator/main.go", Line: 42, Kind: "changed", Scope: "selectSymbols"},
				{Path: candidate, Kind: "file"},
			},
		}
		_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if related := output.(leanResponse).Related; related != nil {
			t.Fatalf("candidate %q produced related action %#v", candidate, related)
		}
	}
}

func TestFindRelatedActionIsDroppedAtomicallyWhenItCannotFit(t *testing.T) {
	response := navigator.FindResponse{
		Query: "validator", MatchedAs: navigator.FindOutcomeFile,
		Results: []navigator.Result{
			{
				Path: "cmd/validator/main.go", Line: 42, Kind: "changed",
				Scope: strings.Repeat("symbol", 100),
			},
			{Path: "cmd/validator/main_test.go", Kind: "file"},
		},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 320)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	if lean.Related != nil || lean.Next != nil || sizing.StructuredBytes > 320 {
		t.Fatalf("tight related response = %#v, sizing %#v", lean, sizing)
	}
}

func TestLeanResultsPreferDistinctFilesAfterBestDefinition(t *testing.T) {
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{
			{Path: "a.go", Line: 30, Kind: "ref", Symbol: "Target"},
			{Path: "a.go", Line: 3, Kind: "def", Symbol: "Target"},
			{Path: "a.go", Line: 40, Kind: "ref", Symbol: "Target"},
			{Path: "b.go", Line: 12, Kind: "ref", Symbol: "Target"},
			{Path: "c.go", Line: 5, Kind: "def", Symbol: "Target"},
			{Path: "d.go", Line: 8, Kind: "ref", Symbol: "Target"},
		},
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lean := output.(leanResponse)
	got := make([]string, 0, len(lean.Results))
	for _, result := range lean.Results {
		got = append(got, result.Location)
	}
	want := []string{"a.go:3", "c.go:5", "b.go:12", "d.go:8", "a.go:30"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diverse definition-first results = %q, want %q", got, want)
	}
	if lean.Selection != selectionAmbiguous || lean.Next != nil ||
		!containsString(lean.Truncated, "results") {
		t.Fatalf("action/truncation = %#v", lean)
	}
}

func TestFindNoMatchHintIsUsefulAndBounded(t *testing.T) {
	response := navigator.FindResponse{
		Query: "missingTarget", MatchedAs: navigator.FindOutcomeNone,
		Results: []navigator.Result{},
	}
	for _, mode := range []string{responseAuto, responseFull} {
		result, _, _, err := prepareToolResponse("find", mode, response, 1024)
		if err != nil {
			t.Fatal(err)
		}
		hint := toolResultText(t, result)
		if hint != noMatchTextHint || len(hint) > maximumCompactTextBytes ||
			strings.Contains(strings.ToLower(hint), "full result") {
			t.Fatalf("%s no-match hint = %q", mode, hint)
		}
	}
}

func TestOutlinePreservesDefinitionSourceOrder(t *testing.T) {
	response := navigator.OutlineResponse{
		Path: "a.go",
		Results: []navigator.Result{
			{Path: "a.go", Line: 20, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Later", Signature: "func Later()"},
			{Path: "a.go", Line: 4, Kind: "def", Finding: navigator.FindingSymbol, Symbol: "Earlier", Signature: "func Earlier()"},
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
	for _, result := range lean.Results {
		if result.Signature != "" {
			t.Fatalf("outline leaked definition source: %#v", lean.Results)
		}
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
	_, output, _, err = prepareToolResponse("inspect", responseAuto, inspect, 1024)
	if err != nil {
		t.Fatal(err)
	}
	bounded := output.(leanResponse)
	if bounded.Target == tooLong || !containsString(bounded.Truncated, "target") ||
		len(mustMarshalResponse(t, bounded)) > 1024 {
		t.Fatalf("long completed target was not transparently bounded: %#v", bounded)
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
	if hint != structuredContentTextHint || strings.Contains(hint, location[:20]) {
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
	for _, candidate := range []string{
		"target", "outcome", "selection", "evidence", "results", "truncated", "next", "next_related", "error",
	} {
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
