package scopesiftermcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

const (
	responseAuto = "auto"
	responseFull = "full"

	maximumLeanResults       = 5
	maximumCompactTextBytes  = 160
	maximumLeanMetadataBytes = 256
	maximumLeanResultBytes   = 128
)

// responseSizing remains internal measurement and adaptive-learning state. No
// size telemetry is emitted in the lean structured response.
type responseSizing struct {
	OriginalBytes   int
	StructuredBytes int
	Compacted       bool
}

// leanResponse is the stable auto-response shape for every tool. Optional
// evidence and next are useful payload, never formatter telemetry.
type leanResponse struct {
	Target    string        `json:"target"`
	Outcome   string        `json:"outcome"`
	Evidence  *leanEvidence `json:"evidence,omitempty"`
	Results   []leanResult  `json:"results"`
	Truncated []string      `json:"truncated"`
	Next      *leanNext     `json:"next,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type leanEvidence struct {
	Kind  string `json:"kind"`
	Start string `json:"start"`
	Text  string `json:"text"`
}

type leanResult struct {
	Location  string `json:"location"`
	Kind      string `json:"kind"`
	Symbol    string `json:"symbol,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type leanNext struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

type leanResultSource struct {
	result navigator.Result
	lean   leanResult
}

type leanEvidenceSource struct {
	start     string
	text      string
	truncated bool
}

type leanResponsePlan struct {
	response         leanResponse
	results          []leanResultSource
	evidence         *leanEvidenceSource
	resultsTruncated bool
	sourceTruncated  bool
}

// prepareToolResponse emits the exact v3 shape only for explicit full calls.
// Auto always emits leanResponse, avoiding a schema that changes with response
// size. Compacted still means the original v3 JSON crossed the adaptive budget.
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
	if mode == responseFull {
		return responseResult(actionableResponseHint(tool, full, nil)), full, sizing, nil
	}

	plan, err := leanResponseFor(tool, full)
	if err != nil {
		return nil, nil, responseSizing{}, err
	}
	if !fitLeanResponse(&plan, tool, budget) {
		return nil, nil, responseSizing{}, fmt.Errorf(
			"%d-byte structured-output budget is too small for exact %s actions",
			budget,
			tool,
		)
	}
	leanJSON, err := json.Marshal(plan.response)
	if err != nil {
		return nil, nil, responseSizing{}, fmt.Errorf("marshal lean %s response: %w", tool, err)
	}
	if len(leanJSON) > budget {
		return nil, nil, responseSizing{}, fmt.Errorf(
			"lean %s response requires %d bytes, exceeds %d-byte budget",
			tool,
			len(leanJSON),
			budget,
		)
	}
	sizing.StructuredBytes = len(leanJSON)
	sizing.Compacted = len(originalJSON) > budget
	return responseResult(actionableResponseHint(tool, full, &plan.response)), plan.response, sizing, nil
}

const fullResponseTextHint = "Full result is in structuredContent."

func responseResult(hint string) *mcp.CallToolResult {
	if hint == "" || len(hint) > maximumCompactTextBytes || !utf8.ValidString(hint) {
		hint = fullResponseTextHint
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: hint}},
	}
}

func leanResponseFor(tool string, full any) (leanResponsePlan, error) {
	plan := leanResponsePlan{response: leanResponse{
		Results:   []leanResult{},
		Truncated: []string{},
	}}
	var results []navigator.Result
	switch response := full.(type) {
	case navigator.ChangedResponse:
		if tool != "changed" {
			return plan, responseTypeError(tool, full)
		}
		plan.response.Target = changedTarget(response.BaseCommit, response.HeadCommit)
		plan.response.Outcome = "unchanged"
		if response.Patch != "" || len(response.Results) != 0 ||
			response.PatchTruncated || response.ResultsTruncated {
			plan.response.Outcome = "changed"
		}
		if response.Patch != "" || response.PatchTruncated {
			markLeanTruncated(&plan.response, "patch")
		}
		plan.resultsTruncated = response.ResultsTruncated
		results = response.Results
	case *navigator.ChangedResponse:
		if response == nil {
			return plan, responseTypeError(tool, full)
		}
		return leanResponseFor(tool, *response)
	case navigator.FindResponse:
		if tool != "find" {
			return plan, responseTypeError(tool, full)
		}
		plan.response.Target, plan.response.Truncated = boundedLeanField(
			response.Query, "target", plan.response.Truncated,
		)
		plan.response.Outcome = string(response.MatchedAs)
		if plan.response.Outcome == "" {
			plan.response.Outcome = string(navigator.FindOutcomeNone)
		}
		plan.resultsTruncated = response.ResultsTruncated
		results = response.Results
	case *navigator.FindResponse:
		if response == nil {
			return plan, responseTypeError(tool, full)
		}
		return leanResponseFor(tool, *response)
	case navigator.InspectResponse:
		if tool != "inspect" {
			return plan, responseTypeError(tool, full)
		}
		// Location is an executable action. Keep it exact or reject the response;
		// silently shortening PATH:LINE would create a false instruction.
		plan.response.Target = response.Location
		plan.response.Outcome, plan.response.Truncated = boundedLeanField(
			response.Symbol, "outcome", plan.response.Truncated,
		)
		if plan.response.Outcome == "" {
			plan.response.Outcome = "scope"
		}
		plan.response.Error, plan.response.Truncated = boundedLeanField(
			response.Error, "error", plan.response.Truncated,
		)
		plan.resultsTruncated = response.ResultsTruncated
		results = response.Results
		if len(results) > 0 && results[0].Code != "" {
			if evidence := inspectEvidenceSource(results[0]); evidence != nil {
				plan.evidence = evidence
				plan.sourceTruncated = evidence.truncated
				results = results[1:]
			}
		}
	case *navigator.InspectResponse:
		if response == nil {
			return plan, responseTypeError(tool, full)
		}
		return leanResponseFor(tool, *response)
	case navigator.OutlineResponse:
		if tool != "outline" {
			return plan, responseTypeError(tool, full)
		}
		plan.response.Target = response.Path
		plan.response.Outcome = "definitions"
		plan.response.Error, plan.response.Truncated = boundedLeanField(
			response.Error, "error", plan.response.Truncated,
		)
		if response.Error != "" {
			plan.response.Outcome = "error"
		}
		plan.resultsTruncated = response.ResultsTruncated
		results = response.Results
	case *navigator.OutlineResponse:
		if response == nil {
			return plan, responseTypeError(tool, full)
		}
		return leanResponseFor(tool, *response)
	default:
		return plan, responseTypeError(tool, full)
	}

	for index := range results {
		result := results[index]
		if result.Code != "" || result.CodeTruncated {
			plan.sourceTruncated = true
		}
		lean, ok := leanResultFor(result)
		if !ok {
			plan.resultsTruncated = true
			continue
		}
		plan.results = append(plan.results, leanResultSource{result: result, lean: lean})
	}
	if tool != "outline" {
		plan.results = definitionFirst(plan.results)
	}
	return plan, nil
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

func changedTarget(base, head string) string {
	if base == "" {
		return head
	}
	if head == "" {
		return base
	}
	return base + ".." + head
}

func boundedLeanField(value, field string, truncated []string) (string, []string) {
	bounded, shortened := boundedJSONString(value, maximumLeanMetadataBytes)
	if shortened {
		truncated = appendUnique(truncated, field)
	}
	return bounded, truncated
}

func inspectEvidenceSource(result navigator.Result) *leanEvidenceSource {
	line := result.CodeStartLine
	if line < 1 {
		line = result.StartLine
	}
	if line < 1 {
		line = actionableResultLine(&result)
	}
	if result.Path == "" || line < 1 || result.Code == "" || !utf8.ValidString(result.Code) {
		return nil
	}
	return &leanEvidenceSource{
		start:     fmt.Sprintf("%s:%d", result.Path, line),
		text:      result.Code,
		truncated: result.CodeTruncated,
	}
}

func leanResultFor(result navigator.Result) (leanResult, bool) {
	if result.Path == "" {
		return leanResult{}, false
	}
	location := result.Path
	if line := actionableResultLine(&result); line > 0 {
		location = fmt.Sprintf("%s:%d", result.Path, line)
	}
	kind := result.Kind
	if kind == "" {
		kind = string(result.Finding)
	}
	symbol, _ := boundedJSONString(result.Symbol, maximumLeanResultBytes)
	return leanResult{Location: location, Kind: kind, Symbol: symbol}, true
}

func definitionFirst(results []leanResultSource) []leanResultSource {
	ordered := make([]leanResultSource, 0, len(results))
	for index := range results {
		if results[index].result.Kind == "def" {
			ordered = append(ordered, results[index])
		}
	}
	for index := range results {
		if results[index].result.Kind != "def" {
			ordered = append(ordered, results[index])
		}
	}
	return ordered
}

func fitLeanResponse(plan *leanResponsePlan, tool string, budget int) bool {
	response := &plan.response
	if plan.resultsTruncated || len(plan.results) > maximumLeanResults {
		markLeanTruncated(response, "results")
	}
	if plan.sourceTruncated {
		markLeanTruncated(response, "source")
	}
	if !leanFits(response, budget) {
		return false
	}

	selected := make([]int, 0, min(len(plan.results), maximumLeanResults))
	primary := firstActionableResult(tool, plan.results)
	if primary >= 0 {
		next := leanNextFor(tool, plan.results[primary].result)
		candidate := coreLeanResult(plan.results[primary].lean)
		if tryPrimaryLeanResult(response, candidate, next, budget) {
			selected = append(selected, primary)
		}
	}

	if plan.evidence != nil {
		fitLeanEvidence(response, plan.evidence, budget)
	}

	for index := range plan.results {
		if len(selected) == maximumLeanResults || containsIndex(selected, index) {
			continue
		}
		candidate := coreLeanResult(plan.results[index].lean)
		if tryLeanResult(response, candidate, budget) {
			selected = append(selected, index)
		}
	}

	if len(selected) != len(plan.results) {
		markLeanTruncated(response, "results")
	}
	if !leanFits(response, budget) {
		// Truncation marker is mandatory. Evict least-important extra results;
		// preserve first actionable result and its exact next when possible.
		for !leanFits(response, budget) && len(response.Results) > 1 {
			response.Results = response.Results[:len(response.Results)-1]
			selected = selected[:len(selected)-1]
		}
		if !leanFits(response, budget) {
			response.Evidence = nil
			markLeanTruncated(response, "source")
		}
		if !leanFits(response, budget) {
			response.Results = []leanResult{}
			response.Next = nil
			selected = selected[:0]
		}
	}
	if !leanFits(response, budget) {
		return false
	}

	// Scope and signature are optional enrichment after exact actions, evidence,
	// and core result identities have fit.
	for resultIndex, sourceIndex := range selected {
		if resultIndex >= len(response.Results) {
			break
		}
		source := plan.results[sourceIndex].result
		for _, optional := range []struct {
			field string
			value string
		}{
			{field: "scope", value: source.Scope},
			{field: "signature", value: source.Signature},
		} {
			field, value := optional.field, optional.value
			value, _ = boundedJSONString(value, maximumLeanResultBytes)
			if value == "" {
				continue
			}
			before := response.Results[resultIndex]
			if field == "scope" {
				response.Results[resultIndex].Scope = value
			} else {
				response.Results[resultIndex].Signature = value
			}
			if !leanFits(response, budget) {
				response.Results[resultIndex] = before
			}
		}
	}
	return leanFits(response, budget)
}

func firstActionableResult(tool string, results []leanResultSource) int {
	for index := range results {
		if leanNextFor(tool, results[index].result) != nil {
			return index
		}
	}
	return -1
}

func leanNextFor(tool string, result navigator.Result) *leanNext {
	if result.Path == "" {
		return nil
	}
	if line := actionableResultLine(&result); line > 0 {
		return &leanNext{Tool: "inspect", Arguments: map[string]any{
			"location": fmt.Sprintf("%s:%d", result.Path, line),
		}}
	}
	if tool == "find" {
		return &leanNext{Tool: "outline", Arguments: map[string]any{
			"path": result.Path,
		}}
	}
	return nil
}

func coreLeanResult(result leanResult) leanResult {
	result.Scope = ""
	result.Signature = ""
	return result
}

func tryPrimaryLeanResult(
	response *leanResponse,
	result leanResult,
	next *leanNext,
	budget int,
) bool {
	if next == nil {
		return false
	}
	response.Results = append(response.Results, result)
	response.Next = next
	if leanFits(response, budget) {
		return true
	}
	response.Results[len(response.Results)-1].Symbol = ""
	if leanFits(response, budget) {
		return true
	}
	response.Results = response.Results[:len(response.Results)-1]
	response.Next = nil
	return false
}

func tryLeanResult(response *leanResponse, result leanResult, budget int) bool {
	response.Results = append(response.Results, result)
	if leanFits(response, budget) {
		return true
	}
	response.Results[len(response.Results)-1].Symbol = ""
	if leanFits(response, budget) {
		return true
	}
	response.Results = response.Results[:len(response.Results)-1]
	return false
}

func fitLeanEvidence(response *leanResponse, source *leanEvidenceSource, budget int) {
	evidence := &leanEvidence{Kind: "source", Start: source.start, Text: source.text}
	response.Evidence = evidence
	if leanFits(response, budget) {
		return
	}
	markLeanTruncated(response, "source")
	lineEnds := completeLineEnds(source.text)
	best := -1
	for low, high := 0, len(lineEnds)-1; low <= high; {
		middle := low + (high-low)/2
		evidence.Text = source.text[:lineEnds[middle]]
		if leanFits(response, budget) {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best >= 0 {
		evidence.Text = source.text[:lineEnds[best]]
		return
	}
	response.Evidence = nil
}

// completeLineEnds returns strict, non-empty prefix ends at source line
// boundaries. Binary-searching these offsets never cuts a UTF-8 rune or line.
func completeLineEnds(text string) []int {
	if text == "" || !utf8.ValidString(text) {
		return nil
	}
	ends := make([]int, 0, strings.Count(text, "\n"))
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' && index > 0 {
			ends = append(ends, index)
		}
	}
	return ends
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

func actionableResponseHint(tool string, full any, lean *leanResponse) string {
	if lean != nil {
		if hint := leanNextHint(lean.Next); hint != "" {
			return hint
		}
		if lean.Evidence != nil {
			return boundedActionHint("Inspect", lean.Evidence.Start)
		}
		return fullResponseTextHint
	}
	plan, err := leanResponseFor(tool, full)
	if err != nil {
		return fullResponseTextHint
	}
	if index := firstActionableResult(tool, plan.results); index >= 0 {
		return leanNextHint(leanNextFor(tool, plan.results[index].result))
	}
	if plan.evidence != nil {
		return boundedActionHint("Inspect", plan.evidence.start)
	}
	return fullResponseTextHint
}

func leanNextHint(next *leanNext) string {
	if next == nil {
		return ""
	}
	switch next.Tool {
	case "inspect":
		location, _ := next.Arguments["location"].(string)
		return boundedActionHint("Inspect", location)
	case "outline":
		path, _ := next.Arguments["path"].(string)
		return boundedActionHint("Outline", path)
	default:
		return ""
	}
}

func boundedActionHint(verb, target string) string {
	if target == "" || !utf8.ValidString(target) || strings.ContainsAny(target, "\r\n") {
		return fullResponseTextHint
	}
	hint := fmt.Sprintf("%s %s; full result is in structuredContent.", verb, target)
	if len(hint) > maximumCompactTextBytes {
		return fullResponseTextHint
	}
	return hint
}

func markLeanTruncated(response *leanResponse, field string) {
	response.Truncated = appendUnique(response.Truncated, field)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func containsIndex(values []int, value int) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func leanFits(response *leanResponse, budget int) bool {
	encoded, err := json.Marshal(response)
	return err == nil && len(encoded) <= budget
}

// boundedJSONString limits encoded JSON bytes, preserving valid UTF-8.
func boundedJSONString(value string, maximum int) (string, bool) {
	encoded, err := json.Marshal(value)
	if err == nil && utf8.ValidString(value) && len(encoded) <= maximum {
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
		if err == nil && utf8.ValidString(candidate) && len(encoded) <= maximum {
			return candidate, true
		}
	}
	return "", true
}
