package scopesiftermcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

const (
	responseAuto = "auto"
	responseFull = "full"

	maximumCompactCandidates  = 5
	maximumCompactFollowups   = 2
	maximumCompactTextBytes   = 160
	maximumCompactValueBytes  = 256
	maximumCandidateTextBytes = 128
)

// responseSizing reports the structured-output bytes observed by the response
// formatter. Adaptive budget learning uses Compacted; the byte counts are also
// useful for protocol-level tests and benchmarks.
type responseSizing struct {
	OriginalBytes   int
	StructuredBytes int
	Compacted       bool
}

// compactToolResponse is the shared v4 compact output variant. Tool-specific
// selector and outcome fields are populated by compactResponseFor. Source code,
// patches, and repository roots intentionally have no representation here.
type compactToolResponse struct {
	Tool              string                `json:"tool"`
	Query             string                `json:"query,omitempty"`
	Location          string                `json:"location,omitempty"`
	Path              string                `json:"path,omitempty"`
	MatchedAs         navigator.FindOutcome `json:"matched_as,omitempty"`
	Symbol            string                `json:"symbol,omitempty"`
	Error             string                `json:"error,omitempty"`
	BaseCommit        string                `json:"base_commit,omitempty"`
	HeadCommit        string                `json:"head_commit,omitempty"`
	HeadSubject       string                `json:"head_subject,omitempty"`
	FullResponse      string                `json:"full_response"`
	Candidates        []compactCandidate    `json:"candidates"`
	Followups         []compactFollowup     `json:"followups"`
	Counts            compactCounts         `json:"counts"`
	OmittedBytes      compactOmittedBytes   `json:"omitted_bytes"`
	OriginalBytes     int                   `json:"original_bytes"`
	BudgetBytes       int                   `json:"budget_bytes"`
	Truncated         compactTruncation     `json:"truncated"`
	Compact           bool                  `json:"compact"`
	MetadataTruncated bool                  `json:"metadata_truncated,omitempty"`
}

type compactCounts struct {
	ReturnedResults int                  `json:"returned_results"`
	UniqueFiles     int                  `json:"unique_files"`
	Findings        compactFindingCounts `json:"findings"`
}

type compactFindingCounts struct {
	File   int `json:"file"`
	Symbol int `json:"symbol"`
	Other  int `json:"other"`
}

type compactTruncation struct {
	Results bool `json:"results"`
	Code    bool `json:"code"`
	Patch   bool `json:"patch"`
}

type compactOmittedBytes struct {
	Code       int `json:"code"`
	Patch      int `json:"patch"`
	Candidates int `json:"candidates"`
}

type compactCandidate struct {
	Location  string            `json:"location"`
	Finding   navigator.Finding `json:"finding"`
	Kind      string            `json:"kind"`
	Symbol    string            `json:"symbol,omitempty"`
	Scope     string            `json:"scope,omitempty"`
	Signature string            `json:"signature,omitempty"`
}

type compactFollowup struct {
	Arguments map[string]any `json:"arguments"`
	Tool      string         `json:"tool"`
}

// prepareToolResponse applies the v4 output contract to one successful v3
// navigator response. Full responses and auto responses within budget retain
// the exact original structured shape. All paths return a short text pointer so
// the MCP SDK does not copy structured JSON into content.
func prepareToolResponse(
	tool, mode string,
	full any,
	budget int,
) (*mcp.CallToolResult, any, responseSizing, error) {
	if mode == "" {
		mode = responseAuto
	}
	if mode != responseAuto && mode != responseFull {
		return nil, nil, responseSizing{}, fmt.Errorf("unsupported response mode %q", mode)
	}
	if mode == responseAuto && budget <= 0 {
		return nil, nil, responseSizing{}, errors.New("structured-output budget must be positive")
	}
	if !validToolResponseType(tool, full) {
		return nil, nil, responseSizing{}, responseTypeError(tool, full)
	}
	originalJSON, err := json.Marshal(full)
	if err != nil {
		return nil, nil, responseSizing{}, fmt.Errorf("marshal full %s response: %w", tool, err)
	}
	sizing := responseSizing{
		OriginalBytes:   len(originalJSON),
		StructuredBytes: len(originalJSON),
	}
	if mode == responseFull || len(originalJSON) <= budget {
		return responseResult(fullResponseTextHint), full, sizing, nil
	}

	compact, candidateSources, err := compactResponseFor(tool, full, len(originalJSON), budget)
	if err != nil {
		return nil, nil, responseSizing{}, err
	}
	if !fitCompactResponse(&compact, candidateSources, budget) {
		return nil, nil, responseSizing{}, fmt.Errorf(
			"%d-byte structured-output budget is too small for compact %s metadata",
			budget,
			tool,
		)
	}
	compactJSON, err := json.Marshal(compact)
	if err != nil {
		return nil, nil, responseSizing{}, fmt.Errorf("marshal compact %s response: %w", tool, err)
	}
	if len(compactJSON) > budget {
		return nil, nil, responseSizing{}, fmt.Errorf(
			"compact %s response requires %d bytes, exceeds %d-byte budget",
			tool,
			len(compactJSON),
			budget,
		)
	}
	sizing.StructuredBytes = len(compactJSON)
	sizing.Compacted = true
	hint := fmt.Sprintf(
		"Structured result compacted from %d to %d bytes; use followups or repeat with response=full.",
		sizing.OriginalBytes,
		sizing.StructuredBytes,
	)
	return responseResult(hint), compact, sizing, nil
}

const fullResponseTextHint = "Structured result is in structuredContent."

func responseResult(hint string) *mcp.CallToolResult {
	if len(hint) > maximumCompactTextBytes {
		hint = hint[:maximumCompactTextBytes]
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: hint}},
	}
}

type compactCandidateSource struct {
	path      string
	candidate compactCandidate
}

func compactResponseFor(
	tool string,
	full any,
	originalBytes, budget int,
) (compactToolResponse, []compactCandidateSource, error) {
	compact := compactToolResponse{
		Compact:       true,
		Tool:          tool,
		OriginalBytes: originalBytes,
		BudgetBytes:   budget,
		Candidates:    []compactCandidate{},
		Followups:     []compactFollowup{},
		FullResponse:  `Repeat the same call with response="full" for complete structured output.`,
	}
	var results []navigator.Result
	switch response := full.(type) {
	case navigator.ChangedResponse:
		if tool != "changed" {
			return compact, nil, responseTypeError(tool, full)
		}
		compact.BaseCommit = response.BaseCommit
		compact.HeadCommit = response.HeadCommit
		compact.HeadSubject, compact.MetadataTruncated = compactValue(response.HeadSubject)
		compact.Truncated.Patch = response.PatchTruncated
		compact.Truncated.Results = response.ResultsTruncated
		compact.OmittedBytes.Patch = len(response.Patch)
		results = response.Results
	case *navigator.ChangedResponse:
		if response == nil {
			return compact, nil, responseTypeError(tool, full)
		}
		return compactResponseFor(tool, *response, originalBytes, budget)
	case navigator.FindResponse:
		if tool != "find" {
			return compact, nil, responseTypeError(tool, full)
		}
		compact.Query, compact.MetadataTruncated = compactValue(response.Query)
		compact.MatchedAs = response.MatchedAs
		compact.Truncated.Results = response.ResultsTruncated
		results = response.Results
	case *navigator.FindResponse:
		if response == nil {
			return compact, nil, responseTypeError(tool, full)
		}
		return compactResponseFor(tool, *response, originalBytes, budget)
	case navigator.InspectResponse:
		if tool != "inspect" {
			return compact, nil, responseTypeError(tool, full)
		}
		var truncated bool
		compact.Location, compact.MetadataTruncated = compactValue(response.Location)
		compact.Symbol, truncated = compactValue(response.Symbol)
		compact.MetadataTruncated = compact.MetadataTruncated || truncated
		compact.Error, truncated = compactValue(response.Error)
		compact.MetadataTruncated = compact.MetadataTruncated || truncated
		compact.Truncated.Results = response.ResultsTruncated
		results = response.Results
	case *navigator.InspectResponse:
		if response == nil {
			return compact, nil, responseTypeError(tool, full)
		}
		return compactResponseFor(tool, *response, originalBytes, budget)
	case navigator.OutlineResponse:
		if tool != "outline" {
			return compact, nil, responseTypeError(tool, full)
		}
		compact.Path, compact.MetadataTruncated = compactValue(response.Path)
		var truncated bool
		compact.Error, truncated = compactValue(response.Error)
		compact.MetadataTruncated = compact.MetadataTruncated || truncated
		compact.Truncated.Results = response.ResultsTruncated
		results = response.Results
	case *navigator.OutlineResponse:
		if response == nil {
			return compact, nil, responseTypeError(tool, full)
		}
		return compactResponseFor(tool, *response, originalBytes, budget)
	default:
		return compact, nil, responseTypeError(tool, full)
	}

	compact.Counts, compact.Truncated.Code, compact.OmittedBytes.Code = summarizeResults(results)
	candidates, candidateBytes := diverseCompactCandidates(results)
	compact.OmittedBytes.Candidates = candidateBytes
	compact.Followups = compactFollowups(tool, results)
	return compact, candidates, nil
}

func responseTypeError(tool string, response any) error {
	return fmt.Errorf("%s response has unsupported type %T", tool, response)
}

func validToolResponseType(tool string, response any) bool {
	switch tool {
	case "changed":
		switch typed := response.(type) {
		case navigator.ChangedResponse:
			return true
		case *navigator.ChangedResponse:
			return typed != nil
		}
	case "find":
		switch typed := response.(type) {
		case navigator.FindResponse:
			return true
		case *navigator.FindResponse:
			return typed != nil
		}
	case "inspect":
		switch typed := response.(type) {
		case navigator.InspectResponse:
			return true
		case *navigator.InspectResponse:
			return typed != nil
		}
	case "outline":
		switch typed := response.(type) {
		case navigator.OutlineResponse:
			return true
		case *navigator.OutlineResponse:
			return typed != nil
		}
	}
	return false
}

func summarizeResults(results []navigator.Result) (compactCounts, bool, int) {
	counts := compactCounts{ReturnedResults: len(results)}
	files := make(map[string]struct{})
	codeTruncated := false
	codeBytes := 0
	for index := range results {
		result := &results[index]
		if result.Path != "" {
			files[result.Path] = struct{}{}
		}
		switch result.Finding {
		case navigator.FindingFile:
			counts.Findings.File++
		case navigator.FindingSymbol:
			counts.Findings.Symbol++
		case navigator.FindingOther:
			counts.Findings.Other++
		default:
			counts.Findings.Other++
		}
		codeBytes += len(result.Code)
		codeTruncated = codeTruncated || result.CodeTruncated
	}
	counts.UniqueFiles = len(files)
	return counts, codeTruncated, codeBytes
}

func diverseCompactCandidates(results []navigator.Result) ([]compactCandidateSource, int) {
	eligible := make([]compactCandidateSource, 0, len(results))
	seenLocations := make(map[string]struct{})
	totalBytes := 0
	for index := range results {
		result := &results[index]
		line := actionableResultLine(result)
		if result.Path == "" {
			continue
		}
		candidate := compactCandidate{
			Location: result.Path,
			Finding:  result.Finding,
			Kind:     result.Kind,
		}
		if line > 0 {
			candidate.Location = fmt.Sprintf("%s:%d", result.Path, line)
		}
		candidate.Symbol, _ = boundedJSONString(result.Symbol, maximumCandidateTextBytes)
		if result.Scope != "" && result.Scope != result.Symbol {
			candidate.Scope, _ = boundedJSONString(result.Scope, maximumCandidateTextBytes)
		}
		candidate.Signature, _ = boundedJSONString(result.Signature, maximumCandidateTextBytes)
		originalCandidate := candidate
		originalCandidate.Symbol = result.Symbol
		if result.Scope != result.Symbol {
			originalCandidate.Scope = result.Scope
		}
		originalCandidate.Signature = result.Signature
		originalEncoded, err := json.Marshal(originalCandidate)
		if err != nil {
			continue
		}
		totalBytes += len(originalEncoded)
		if line < 1 {
			// A file without a line can inform an outline follow-up, but cannot be
			// presented as a path:line candidate.
			continue
		}
		if _, duplicate := seenLocations[candidate.Location]; duplicate {
			continue
		}
		seenLocations[candidate.Location] = struct{}{}
		if _, err := json.Marshal(candidate); err != nil {
			continue
		}
		eligible = append(eligible, compactCandidateSource{
			path:      result.Path,
			candidate: candidate,
		})
	}

	ordered := make([]compactCandidateSource, 0, len(eligible))
	selected := make([]bool, len(eligible))
	seenPaths := make(map[string]struct{})
	for index := range eligible {
		source := &eligible[index]
		if _, seen := seenPaths[source.path]; seen {
			continue
		}
		seenPaths[source.path] = struct{}{}
		selected[index] = true
		ordered = append(ordered, *source)
	}
	for index := range eligible {
		if !selected[index] {
			ordered = append(ordered, eligible[index])
		}
	}
	return ordered, totalBytes
}

func compactFollowups(tool string, results []navigator.Result) []compactFollowup {
	followups := make([]compactFollowup, 0, maximumCompactFollowups)
	start := 0
	if tool == "inspect" && len(results) > 0 {
		// Inspect's first result is the scope at the requested location. Repeating
		// the same compacting call is not progressive disclosure; prefer a related
		// result when one exists.
		start = 1
	}
	for index := start; index < len(results); index++ {
		result := &results[index]
		line := actionableResultLine(result)
		if result.Path != "" && line > 0 {
			followups = append(followups, compactFollowup{
				Tool: "inspect",
				Arguments: map[string]any{
					"location": fmt.Sprintf("%s:%d", result.Path, line),
				},
			})
			break
		}
	}
	if len(followups) < maximumCompactFollowups && tool != "outline" {
		for index := range results {
			result := &results[index]
			if result.Path == "" {
				continue
			}
			// Path-only find results refer to current repository files. Changed
			// path-only results may represent a deletion or binary file.
			if actionableResultLine(result) == 0 && tool != "find" {
				continue
			}
			followups = append(followups, compactFollowup{
				Tool: "outline",
				Arguments: map[string]any{
					"path": result.Path,
				},
			})
			break
		}
	}
	return followups
}

func actionableResultLine(result *navigator.Result) int {
	if result.Line > 0 {
		return result.Line
	}
	if result.StartLine > 0 {
		return result.StartLine
	}
	for _, line := range result.ChangedLines {
		if line > 0 {
			return line
		}
	}
	return 0
}

func fitCompactResponse(
	compact *compactToolResponse,
	candidates []compactCandidateSource,
	budget int,
) bool {
	wantedFollowups := compact.Followups
	compact.Followups = []compactFollowup{}

	// Selectors are routing-critical, but a caller may use the full 4096-byte
	// input allowance while the compact budget is only 1 KiB. Tighten metadata
	// as a group before considering optional follow-ups and candidates.
	if !fitCompactMetadata(compact, budget) {
		return false
	}

	// Follow-ups are the escape hatch for progressive disclosure, so retain as
	// many as fit before filling the remaining space with ranked candidates.
	for _, followup := range wantedFollowups {
		if len(compact.Followups) == maximumCompactFollowups {
			break
		}
		compact.Followups = append(compact.Followups, followup)
		if !compactFits(compact, budget) {
			compact.Followups = compact.Followups[:len(compact.Followups)-1]
		}
	}

	for index := range candidates {
		if len(compact.Candidates) == maximumCompactCandidates {
			break
		}
		candidate := candidates[index].candidate
		if !tryCompactCandidate(compact, &candidate, budget) {
			candidate.Signature = ""
			if !tryCompactCandidate(compact, &candidate, budget) {
				candidate.Scope = ""
				if !tryCompactCandidate(compact, &candidate, budget) {
					candidate.Symbol = ""
					if !tryCompactCandidate(compact, &candidate, budget) {
						continue
					}
				}
			}
		}
	}
	return compactFits(compact, budget)
}

func fitCompactMetadata(compact *compactToolResponse, budget int) bool {
	for _, maximum := range []int{128, 64, 32, 16, 8, 2} {
		if compactFits(compact, budget) {
			return true
		}
		truncateCompactMetadata(compact, maximum)
	}
	if compactFits(compact, budget) {
		return true
	}
	// Outcome annotations are useful but less critical than the tool target,
	// exact commit IDs, aggregate counts, and retry instructions.
	compact.Error = ""
	compact.Symbol = ""
	compact.HeadSubject = ""
	return compactFits(compact, budget)
}

func truncateCompactMetadata(compact *compactToolResponse, maximum int) {
	var truncated bool
	compact.Query, truncated = boundedJSONString(compact.Query, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
	compact.Location, truncated = boundedJSONString(compact.Location, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
	compact.Path, truncated = boundedJSONString(compact.Path, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
	compact.Symbol, truncated = boundedJSONString(compact.Symbol, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
	compact.Error, truncated = boundedJSONString(compact.Error, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
	compact.HeadSubject, truncated = boundedJSONString(compact.HeadSubject, maximum)
	compact.MetadataTruncated = compact.MetadataTruncated || truncated
}

func tryCompactCandidate(
	compact *compactToolResponse,
	candidate *compactCandidate,
	budget int,
) bool {
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return false
	}
	compact.Candidates = append(compact.Candidates, *candidate)
	compact.OmittedBytes.Candidates -= len(encoded)
	if compactFits(compact, budget) {
		return true
	}
	compact.Candidates = compact.Candidates[:len(compact.Candidates)-1]
	compact.OmittedBytes.Candidates += len(encoded)
	return false
}

func compactFits(compact *compactToolResponse, budget int) bool {
	encoded, err := json.Marshal(compact)
	return err == nil && len(encoded) <= budget
}

func compactValue(value string) (string, bool) {
	return boundedJSONString(value, maximumCompactValueBytes)
}

// boundedJSONString limits the encoded JSON string rather than the raw input,
// so quotes, control characters, and multibyte text cannot evade the bound.
func boundedJSONString(value string, maximum int) (string, bool) {
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= maximum {
		return value, false
	}
	const suffix = "…"
	for value != "" {
		_, size := utf8.DecodeLastRuneInString(value)
		if size == 0 {
			break
		}
		value = value[:len(value)-size]
		candidate := value + suffix
		encoded, err = json.Marshal(candidate)
		if err == nil && len(encoded) <= maximum {
			return candidate, true
		}
	}
	return "", true
}
