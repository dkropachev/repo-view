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

func TestPrepareToolResponseSwitchesOnlyAboveBudget(t *testing.T) {
	responses := []struct {
		name     string
		tool     string
		response any
	}{
		{
			name: "changed",
			tool: "changed",
			response: navigator.ChangedResponse{
				BaseCommit: "base", HeadCommit: "head", Patch: strings.Repeat("patch\n", 400),
				Results: largeResponseResults(),
			},
		},
		{
			name: "find",
			tool: "find",
			response: navigator.FindResponse{
				Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
				Results: largeResponseResults(),
			},
		},
		{
			name: "inspect",
			tool: "inspect",
			response: navigator.InspectResponse{
				Location: "first.go:7", Symbol: "Target", Results: largeResponseResults(),
			},
		},
		{
			name: "outline",
			tool: "outline",
			response: navigator.OutlineResponse{
				Path: "first.go", Results: largeResponseResults(),
			},
		},
	}
	for _, testCase := range responses {
		t.Run(testCase.name, func(t *testing.T) {
			fullJSON := mustMarshalResponse(t, testCase.response)
			result, output, sizing, err := prepareToolResponse(
				testCase.tool,
				responseAuto,
				testCase.response,
				len(fullJSON),
			)
			if err != nil {
				t.Fatal(err)
			}
			if sizing.Compacted || sizing.OriginalBytes != len(fullJSON) ||
				sizing.StructuredBytes != len(fullJSON) {
				t.Fatalf("within-budget sizing = %#v", sizing)
			}
			if reflect.TypeOf(output) != reflect.TypeOf(testCase.response) {
				t.Fatalf("within-budget type = %T, want %T", output, testCase.response)
			}
			if got := mustMarshalResponse(t, output); string(got) != string(fullJSON) {
				t.Fatalf("within-budget output changed:\n%s\nwant:\n%s", got, fullJSON)
			}
			assertBoundedNonJSONHint(t, result, fullJSON)

			result, output, sizing, err = prepareToolResponse(
				testCase.tool,
				responseAuto,
				testCase.response,
				len(fullJSON)-1,
			)
			if err != nil {
				t.Fatal(err)
			}
			compact, ok := output.(compactToolResponse)
			if !ok || !compact.Compact || !sizing.Compacted {
				t.Fatalf("over-budget output = %T %#v, sizing %#v", output, output, sizing)
			}
			compactJSON := mustMarshalResponse(t, compact)
			if len(compactJSON) > len(fullJSON)-1 || sizing.StructuredBytes != len(compactJSON) {
				t.Fatalf("compact bytes = %d, sizing %#v, budget %d", len(compactJSON), sizing, len(fullJSON)-1)
			}
			assertBoundedNonJSONHint(t, result, compactJSON)
		})
	}
}

func TestPrepareToolResponseAtEveryAdaptiveBudgetBoundary(t *testing.T) {
	for _, budget := range []int{1024, 1536, 2048, 2560, 3072} {
		t.Run(fmt.Sprintf("%d", budget), func(t *testing.T) {
			belowBoundary := findResponseWithSerializedBytes(t, budget-1)
			_, output, sizing, err := prepareToolResponse("find", responseAuto, belowBoundary, budget)
			if err != nil {
				t.Fatal(err)
			}
			if sizing.Compacted || reflect.TypeOf(output) != reflect.TypeOf(belowBoundary) {
				t.Fatalf("%d-byte response compacted below %d-byte budget: %T %#v", budget-1, budget, output, sizing)
			}

			atBoundary := findResponseWithSerializedBytes(t, budget)
			_, output, sizing, err = prepareToolResponse("find", responseAuto, atBoundary, budget)
			if err != nil {
				t.Fatal(err)
			}
			if sizing.Compacted || reflect.TypeOf(output) != reflect.TypeOf(atBoundary) {
				t.Fatalf("%d-byte response compacted at %d-byte budget: %T %#v", budget, budget, output, sizing)
			}

			overBoundary := findResponseWithSerializedBytes(t, budget+1)
			_, output, sizing, err = prepareToolResponse("find", responseAuto, overBoundary, budget)
			if err != nil {
				t.Fatal(err)
			}
			compact, ok := output.(compactToolResponse)
			if !ok || !sizing.Compacted {
				t.Fatalf("%d-byte response did not compact above %d-byte budget: %T %#v", budget+1, budget, output, sizing)
			}
			if got := len(mustMarshalResponse(t, compact)); got > budget {
				t.Fatalf("compact boundary response = %d bytes, budget %d", got, budget)
			}
		})
	}
}

func TestPrepareToolResponseFullPreservesV3ShapeAndIgnoresAdaptiveBudget(t *testing.T) {
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
	wantJSON := mustMarshalResponse(t, response)
	result, output, sizing, err := prepareToolResponse("find", responseFull, response, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sizing.Compacted || sizing.OriginalBytes != len(wantJSON) ||
		sizing.StructuredBytes != len(wantJSON) {
		t.Fatalf("full sizing = %#v", sizing)
	}
	actual, ok := output.(navigator.FindResponse)
	if !ok || !reflect.DeepEqual(actual, response) {
		t.Fatalf("full output = %T %#v, want exact navigator.FindResponse", output, output)
	}
	if gotJSON := mustMarshalResponse(t, output); string(gotJSON) != string(wantJSON) {
		t.Fatalf("full JSON changed:\n%s\nwant:\n%s", gotJSON, wantJSON)
	}
	assertBoundedNonJSONHint(t, result, wantJSON)
}

func TestCompactResponseFitsOneKiBBudgetAndSummarizesAllResults(t *testing.T) {
	const (
		budget      = 1024
		secretCode  = "SOURCE-MUST-NOT-APPEAR"
		secretPatch = "PATCH-MUST-NOT-APPEAR"
	)
	results := []navigator.Result{
		compactTestResult("a.go", 3, navigator.FindingSymbol, "def", "Alpha", "Package", secretCode),
		compactTestResult("a.go", 8, navigator.FindingOther, "imports", "", "", secretCode),
		compactTestResult("b.go", 9, navigator.FindingFile, "changed", "", "Beta", secretCode),
		compactTestResult("c.go", 12, navigator.FindingSymbol, "ref", "Alpha", "Caller", secretCode),
		compactTestResult("d.go", 15, navigator.FindingSymbol, "ref", "Alpha", "Caller", secretCode),
		compactTestResult("e.go", 18, navigator.FindingSymbol, "ref", "Alpha", "Caller", secretCode),
		compactTestResult("f.go", 21, navigator.FindingOther, "changed", "", "", secretCode),
	}
	results[0].Signature = "func Alpha()"
	results[3].CodeTruncated = true
	response := navigator.ChangedResponse{
		Root:             "/repository",
		Base:             "main",
		BaseCommit:       strings.Repeat("a", 40),
		HeadCommit:       strings.Repeat("b", 40),
		HeadSubject:      "compact output",
		Patch:            strings.Repeat(secretPatch, 200),
		Results:          results,
		PatchTruncated:   true,
		ResultsTruncated: true,
	}
	result, output, sizing, err := prepareToolResponse("changed", responseAuto, response, budget)
	if err != nil {
		t.Fatal(err)
	}
	compact, ok := output.(compactToolResponse)
	if !ok {
		t.Fatalf("output type = %T, want compactToolResponse", output)
	}
	encoded := mustMarshalResponse(t, compact)
	if len(encoded) > budget || sizing.StructuredBytes != len(encoded) {
		t.Fatalf("compact size = %d, sizing %#v, budget %d", len(encoded), sizing, budget)
	}
	if !sizing.Compacted || compact.OriginalBytes != sizing.OriginalBytes ||
		compact.BudgetBytes != budget {
		t.Fatalf("compact metadata = %#v, sizing %#v", compact, sizing)
	}
	wantCounts := compactCounts{
		ReturnedResults: 7,
		UniqueFiles:     6,
		Findings: compactFindingCounts{
			File: 1, Symbol: 4, Other: 2,
		},
	}
	if compact.Counts != wantCounts {
		t.Fatalf("counts = %#v, want %#v", compact.Counts, wantCounts)
	}
	if compact.OmittedBytes.Code != len(secretCode)*len(results) ||
		compact.OmittedBytes.Patch != len(response.Patch) ||
		compact.OmittedBytes.Candidates <= 0 {
		t.Fatalf("omissions = %#v", compact.OmittedBytes)
	}
	if compact.Truncated != (compactTruncation{Results: true, Code: true, Patch: true}) {
		t.Fatalf("truncation = %#v", compact.Truncated)
	}
	if compact.BaseCommit != response.BaseCommit || compact.HeadCommit != response.HeadCommit ||
		compact.HeadSubject != response.HeadSubject {
		t.Fatalf("changed outcome metadata = %#v", compact)
	}
	if strings.Contains(string(encoded), secretCode) || strings.Contains(string(encoded), secretPatch) ||
		strings.Contains(string(encoded), response.Root) {
		t.Fatalf("compact response leaked omitted content: %s", encoded)
	}
	if len(compact.Candidates) == 0 || len(compact.Candidates) > maximumCompactCandidates {
		t.Fatalf("candidates = %#v", compact.Candidates)
	}
	seen := make(map[string]bool)
	for _, candidate := range compact.Candidates {
		if !validCompactLocation(candidate.Location) {
			t.Fatalf("candidate has incomplete actionable location: %#v", candidate)
		}
		if seen[candidate.Location] {
			t.Fatalf("duplicate candidate = %#v", candidate)
		}
		seen[candidate.Location] = true
	}
	if compact.Candidates[0].Location != "a.go:3" {
		t.Fatalf("first candidate = %#v, want highest-ranked result", compact.Candidates[0])
	}
	assertExactFollowups(t, compact.Followups)
	if !strings.Contains(compact.FullResponse, `response="full"`) {
		t.Fatalf("full-response instruction = %q", compact.FullResponse)
	}
	assertBoundedNonJSONHint(t, result, encoded)
}

func TestCompactCandidatesPreferDistinctFilesInRankOrder(t *testing.T) {
	largeCode := strings.Repeat("code ", 200)
	results := []navigator.Result{
		compactTestResult("a.go", 1, navigator.FindingSymbol, "def", "A", "", largeCode),
		compactTestResult("a.go", 1, navigator.FindingSymbol, "def", "A", "", largeCode),
		compactTestResult("a.go", 2, navigator.FindingSymbol, "ref", "A", "", largeCode),
		compactTestResult("b.go", 3, navigator.FindingSymbol, "ref", "A", "", largeCode),
		compactTestResult("c.go", 4, navigator.FindingSymbol, "ref", "A", "", largeCode),
		compactTestResult("d.go", 5, navigator.FindingSymbol, "ref", "A", "", largeCode),
		compactTestResult("e.go", 6, navigator.FindingSymbol, "ref", "A", "", largeCode),
	}
	response := navigator.FindResponse{
		Query: "A", MatchedAs: navigator.FindOutcomeSymbol, Results: results,
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 3<<10)
	if err != nil {
		t.Fatal(err)
	}
	compact := output.(compactToolResponse)
	want := []string{"a.go:1", "b.go:3", "c.go:4", "d.go:5", "e.go:6"}
	got := make([]string, 0, len(compact.Candidates))
	for _, candidate := range compact.Candidates {
		got = append(got, candidate.Location)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate locations = %q, want distinct paths in rank order %q", got, want)
	}
}

func TestCompactResponseAlwaysFitsOneKiBWithWorstCaseMetadata(t *testing.T) {
	longValue := strings.Repeat("界\"\n", 1400)
	response := navigator.InspectResponse{
		Location: longValue,
		Symbol:   longValue,
		Error:    longValue,
		Results: []navigator.Result{
			compactTestResult(longValue, 42, navigator.FindingSymbol, "ref", longValue, longValue, strings.Repeat("source", 1000)),
		},
	}
	_, output, sizing, err := prepareToolResponse("inspect", responseAuto, response, 1024)
	if err != nil {
		t.Fatalf("worst-case compact response failed: %v", err)
	}
	compact := output.(compactToolResponse)
	encoded := mustMarshalResponse(t, compact)
	if len(encoded) > 1024 || sizing.StructuredBytes != len(encoded) {
		t.Fatalf("worst-case compact size = %d, sizing %#v", len(encoded), sizing)
	}
	if !compact.MetadataTruncated {
		t.Fatalf("metadata truncation was not exposed: %#v", compact)
	}
	if len(compact.Candidates) != 0 || len(compact.Followups) != 0 {
		t.Fatalf("incomplete oversized actions survived: candidates=%#v followups=%#v", compact.Candidates, compact.Followups)
	}
}

func TestCompactCandidateRetainsLocationBeforeOversizedAncillaryFields(t *testing.T) {
	longValue := strings.Repeat("界\"\n", 400)
	result := compactTestResult(
		"small.go",
		17,
		navigator.FindingSymbol,
		"ref",
		longValue,
		longValue,
		strings.Repeat("source", 1000),
	)
	result.Signature = longValue
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol,
		Results: []navigator.Result{result},
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	compact := output.(compactToolResponse)
	if len(compact.Candidates) != 1 || compact.Candidates[0].Location != "small.go:17" {
		t.Fatalf("actionable candidate was lost: %#v", compact.Candidates)
	}
	if compact.OmittedBytes.Candidates <= len(mustMarshalResponse(t, compact.Candidates[0])) {
		t.Fatalf("truncated ancillary bytes were not counted: %#v", compact.OmittedBytes)
	}
	if strings.Contains(string(mustMarshalResponse(t, compact)), longValue) {
		t.Fatal("unbounded ancillary candidate content survived")
	}
}

func TestMetadataTruncationIncludesOptionalOutcomeFields(t *testing.T) {
	longValue := strings.Repeat("error\"\n", 300)
	responses := []struct {
		tool     string
		response any
	}{
		{
			tool: "inspect",
			response: navigator.InspectResponse{
				Location: "small.go:1", Symbol: longValue, Error: longValue,
				Results: []navigator.Result{},
			},
		},
		{
			tool: "outline",
			response: navigator.OutlineResponse{
				Path: "small.go", Error: longValue, Results: []navigator.Result{},
			},
		},
	}
	for _, testCase := range responses {
		t.Run(testCase.tool, func(t *testing.T) {
			_, output, _, err := prepareToolResponse(testCase.tool, responseAuto, testCase.response, 1024)
			if err != nil {
				t.Fatal(err)
			}
			compact := output.(compactToolResponse)
			if !compact.MetadataTruncated {
				t.Fatalf("optional outcome truncation was not exposed: %#v", compact)
			}
		})
	}
}

func TestCompactResponseRejectsIncompleteCandidateLocation(t *testing.T) {
	longPath := strings.Repeat("deep/", 300) + "file.go"
	result := navigator.Result{
		Path: longPath, Finding: navigator.FindingSymbol, Kind: "ref", Symbol: "Target",
		Line: 4, StartLine: 4, EndLine: 4, Code: strings.Repeat("source", 300),
	}
	response := navigator.FindResponse{
		Query: "Target", MatchedAs: navigator.FindOutcomeSymbol, Results: []navigator.Result{result},
	}
	_, output, sizing, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	compact := output.(compactToolResponse)
	encoded := mustMarshalResponse(t, compact)
	if len(encoded) > 1024 || !sizing.Compacted {
		t.Fatalf("compact size = %d, sizing %#v", len(encoded), sizing)
	}
	if len(compact.Candidates) != 0 || len(compact.Followups) != 0 {
		t.Fatalf("oversize actionable locations retained: candidates=%#v followups=%#v", compact.Candidates, compact.Followups)
	}
	if strings.Contains(string(encoded), longPath) || compact.OmittedBytes.Candidates == 0 {
		t.Fatalf("oversize path was not wholly omitted: %s", encoded)
	}
}

func TestCompactCandidateFallsBackToChangedLine(t *testing.T) {
	response := navigator.ChangedResponse{
		Patch: strings.Repeat("patch", 400),
		Results: []navigator.Result{{
			Path:         "changed.go",
			Finding:      navigator.FindingOther,
			Kind:         "changed",
			ChangedLines: []int{0, 23, 24},
			Code:         strings.Repeat("source", 400),
		}},
	}
	_, output, _, err := prepareToolResponse("changed", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	compact := output.(compactToolResponse)
	if len(compact.Candidates) != 1 || compact.Candidates[0].Location != "changed.go:23" {
		t.Fatalf("changed-lines candidate = %#v", compact.Candidates)
	}
	if len(compact.Followups) == 0 || compact.Followups[0].Tool != "inspect" ||
		compact.Followups[0].Arguments["location"] != "changed.go:23" {
		t.Fatalf("changed-lines followup = %#v", compact.Followups)
	}
}

func TestCompactPathFindingOffersExactOutlineFollowup(t *testing.T) {
	response := navigator.FindResponse{
		Query: "service", MatchedAs: navigator.FindOutcomeFile,
		Results: []navigator.Result{{
			Path: "src/service.go", Finding: navigator.FindingFile, Kind: "file",
		}},
		Hint: strings.Repeat("large legacy hint ", 100),
	}
	_, output, _, err := prepareToolResponse("find", responseAuto, response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	compact := output.(compactToolResponse)
	if compact.Query != response.Query || compact.MatchedAs != navigator.FindOutcomeFile {
		t.Fatalf("find metadata = %#v", compact)
	}
	if len(compact.Candidates) != 0 {
		t.Fatalf("path-only result became inactionable line candidate: %#v", compact.Candidates)
	}
	if compact.OmittedBytes.Candidates == 0 {
		t.Fatalf("path-only result omission was not exposed: %#v", compact.OmittedBytes)
	}
	want := []compactFollowup{{
		Tool: "outline", Arguments: map[string]any{"path": "src/service.go"},
	}}
	if !reflect.DeepEqual(compact.Followups, want) {
		t.Fatalf("path followups = %#v, want %#v", compact.Followups, want)
	}
}

func TestCompactResponseToolSpecificTargetsAndBoundedMetadata(t *testing.T) {
	longValue := strings.Repeat("界\"\n", 200)
	responses := []struct {
		tool     string
		response any
		assert   func(*testing.T, compactToolResponse)
	}{
		{
			tool: "find",
			response: navigator.FindResponse{
				Query: longValue, MatchedAs: navigator.FindOutcomeOther,
				Hint: strings.Repeat("inflate", 300), Results: []navigator.Result{},
			},
			assert: func(t *testing.T, compact compactToolResponse) {
				t.Helper()
				if compact.Query == "" || compact.MatchedAs != navigator.FindOutcomeOther {
					t.Fatalf("find compact metadata = %#v", compact)
				}
			},
		},
		{
			tool: "inspect",
			response: navigator.InspectResponse{
				Location: longValue, Symbol: "Target", Error: strings.Repeat("error", 400),
				Results: []navigator.Result{},
			},
			assert: func(t *testing.T, compact compactToolResponse) {
				t.Helper()
				if compact.Location == "" || compact.Symbol != "Target" || compact.Error == "" {
					t.Fatalf("inspect compact metadata = %#v", compact)
				}
			},
		},
		{
			tool: "outline",
			response: navigator.OutlineResponse{
				Path: longValue, Error: strings.Repeat("error", 400), Results: []navigator.Result{},
			},
			assert: func(t *testing.T, compact compactToolResponse) {
				t.Helper()
				if compact.Path == "" || compact.Error == "" {
					t.Fatalf("outline compact metadata = %#v", compact)
				}
			},
		},
	}
	for _, testCase := range responses {
		t.Run(testCase.tool, func(t *testing.T) {
			_, output, _, err := prepareToolResponse(testCase.tool, responseAuto, testCase.response, 1024)
			if err != nil {
				t.Fatal(err)
			}
			compact := output.(compactToolResponse)
			testCase.assert(t, compact)
			if !compact.MetadataTruncated {
				t.Fatalf("metadata truncation not exposed: %#v", compact)
			}
			encoded := mustMarshalResponse(t, compact)
			if len(encoded) > 1024 || !utf8.Valid(encoded) {
				t.Fatalf("compact metadata encoding is invalid or oversized: %d bytes", len(encoded))
			}
		})
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

func assertBoundedNonJSONHint(t *testing.T, result *mcp.CallToolResult, structuredJSON []byte) {
	t.Helper()
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("content = %#v, want one text hint", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text == "" || len(text.Text) > maximumCompactTextBytes {
		t.Fatalf("text hint = %#v", result.Content[0])
	}
	if text.Text == string(structuredJSON) || strings.Contains(text.Text, string(structuredJSON)) ||
		strings.HasPrefix(strings.TrimSpace(text.Text), "{") {
		t.Fatalf("text duplicated structured JSON: %q", text.Text)
	}
}

func assertExactFollowups(t *testing.T, followups []compactFollowup) {
	t.Helper()
	if len(followups) == 0 || len(followups) > maximumCompactFollowups {
		t.Fatalf("followups = %#v", followups)
	}
	for _, followup := range followups {
		switch followup.Tool {
		case "inspect":
			if len(followup.Arguments) != 1 {
				t.Fatalf("inspect followup arguments = %#v", followup.Arguments)
			}
			location, ok := followup.Arguments["location"].(string)
			if !ok || !validCompactLocation(location) {
				t.Fatalf("inspect followup = %#v", followup)
			}
		case "outline":
			if len(followup.Arguments) != 1 {
				t.Fatalf("outline followup arguments = %#v", followup.Arguments)
			}
			path, ok := followup.Arguments["path"].(string)
			if !ok || path == "" || strings.Contains(path, ":") {
				t.Fatalf("outline followup = %#v", followup)
			}
		default:
			t.Fatalf("unsupported compact followup = %#v", followup)
		}
	}
}

func validCompactLocation(location string) bool {
	separator := strings.LastIndexByte(location, ':')
	if separator < 1 || separator == len(location)-1 {
		return false
	}
	for _, digit := range location[separator+1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return location[separator+1:] != "0"
}
