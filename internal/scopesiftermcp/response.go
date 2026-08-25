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
	selectionUnique     = "unique"
	selectionAmbiguous  = "ambiguous"
	selectionIncomplete = "incomplete"

	maximumLeanResults       = 5
	maximumCompactTextBytes  = 160
	maximumLeanMetadataBytes = 256
	maximumLeanResultBytes   = 128
)

// responseSizing is internal measurement state. No size telemetry is emitted
// in the lean structured response.
type responseSizing struct {
	OriginalBytes   int
	StructuredBytes int
	Compacted       bool
}

// leanResponse is the stable response shape for every tool. Optional
// evidence is useful payload, never formatter telemetry.
//
//nolint:govet,nolintlint // Declaration order is the intentional model-visible JSON order.
type leanResponse struct {
	Target    string        `json:"target"`
	Outcome   string        `json:"outcome"`
	Selection string        `json:"selection,omitempty"`
	Evidence  *leanEvidence `json:"evidence,omitempty"`
	Results   []leanResult  `json:"results"`
	Truncated []string      `json:"truncated"`
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

type leanResultSource struct {
	lean   leanResult
	result navigator.Result
}

type leanEvidenceSource struct {
	path      string
	text      string
	startLine int
	truncated bool
}

type leanResponsePlan struct {
	evidence         *leanEvidenceSource
	inspectFallback  *leanResultSource
	response         leanResponse
	results          []leanResultSource
	resultsTruncated bool
	sourceTruncated  bool
}

// prepareToolResponse always emits the fixed lean response shape. Compacted
// means the navigator JSON crossed the supplied fixed budget.
func prepareToolResponse(
	tool string,
	navigatorResponse any,
	budget int,
) (*mcp.CallToolResult, any, responseSizing, error) {
	if budget <= 0 {
		return nil, nil, responseSizing{}, errors.New("structured-output budget must be positive")
	}
	if !validToolResponseType(tool, navigatorResponse) {
		return nil, nil, responseSizing{}, responseTypeError(tool, navigatorResponse)
	}
	originalJSON, err := json.Marshal(navigatorResponse)
	if err != nil {
		return nil, nil, responseSizing{}, fmt.Errorf("marshal navigator %s response: %w", tool, err)
	}
	sizing := responseSizing{
		OriginalBytes:   len(originalJSON),
		StructuredBytes: len(originalJSON),
	}
	plan, err := leanResponseFor(tool, navigatorResponse)
	if err != nil {
		return nil, nil, responseSizing{}, err
	}
	if !fitLeanResponse(&plan, tool, budget) {
		return nil, nil, responseSizing{}, fmt.Errorf(
			"%d-byte structured-output budget is too small for exact %s candidates",
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
	return responseResult(), plan.response, sizing, nil
}

const structuredContentTextHint = "See structuredContent."

func responseResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: structuredContentTextHint}},
	}
}

func leanResponseFor(tool string, navigatorResponse any) (leanResponsePlan, error) {
	plan := leanResponsePlan{response: leanResponse{
		Results:   []leanResult{},
		Truncated: []string{},
	}}
	var results []navigator.Result
	switch response := navigatorResponse.(type) {
	case navigator.ChangedResponse:
		if tool != "changed" {
			return plan, responseTypeError(tool, navigatorResponse)
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
			return plan, responseTypeError(tool, navigatorResponse)
		}
		return leanResponseFor(tool, *response)
	case navigator.FindResponse:
		if tool != "find" {
			return plan, responseTypeError(tool, navigatorResponse)
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
			return plan, responseTypeError(tool, navigatorResponse)
		}
		return leanResponseFor(tool, *response)
	case navigator.InspectResponse:
		if tool != "inspect" {
			return plan, responseTypeError(tool, navigatorResponse)
		}
		// Target describes the completed call. Bound it as metadata while
		// preserving every emitted result location exactly.
		plan.response.Target, plan.response.Truncated = boundedLeanField(
			response.Location, "target", plan.response.Truncated,
		)
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
		results = append([]navigator.Result(nil), response.Results...)
		if primary := primaryInspectScope(response.Location, results); primary >= 0 {
			primaryLocation := ""
			if lean, ok := leanResultFor(results[primary]); ok {
				primaryLocation = lean.Location
				fallback := leanResultSource{result: results[primary], lean: lean}
				plan.inspectFallback = &fallback
			}
			if evidence := inspectEvidenceSource(results[primary]); evidence != nil {
				if evidence.truncated {
					plan.sourceTruncated = true
				} else {
					plan.evidence = evidence
				}
			} else {
				plan.sourceTruncated = true
			}
			// Inspecting the scope that was just inspected wastes a turn. Exclude
			// it even when locations-only output supplied no source evidence. Any
			// related result at the same action is redundant for the same reason.
			filtered := make([]navigator.Result, 0, len(results)-1)
			for index := range results {
				lean, actionable := leanResultFor(results[index])
				if index == primary ||
					(primaryLocation != "" && actionable && lean.Location == primaryLocation) {
					continue
				}
				filtered = append(filtered, results[index])
			}
			results = filtered
		}
	case *navigator.InspectResponse:
		if response == nil {
			return plan, responseTypeError(tool, navigatorResponse)
		}
		return leanResponseFor(tool, *response)
	case navigator.OutlineResponse:
		if tool != "outline" {
			return plan, responseTypeError(tool, navigatorResponse)
		}
		plan.response.Target, plan.response.Truncated = boundedLeanField(
			response.Path, "target", plan.response.Truncated,
		)
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
			return plan, responseTypeError(tool, navigatorResponse)
		}
		return leanResponseFor(tool, *response)
	default:
		return plan, responseTypeError(tool, navigatorResponse)
	}

	for index := range results {
		result := results[index]
		if result.Code != "" || result.CodeTruncated {
			evidence := inspectEvidenceSource(result)
			if plan.evidence == nil || evidence == nil ||
				evidence.path != plan.evidence.path ||
				evidence.startLine != plan.evidence.startLine ||
				evidence.text != plan.evidence.text {
				plan.sourceTruncated = true
			}
		}
		lean, ok := leanResultFor(result)
		if !ok {
			plan.resultsTruncated = true
			continue
		}
		plan.results = append(plan.results, leanResultSource{result: result, lean: lean})
	}
	if tool != "outline" {
		plan.results = distinctFilesFirst(definitionFirst(plan.results))
	}
	if tool == "find" {
		normalized := make([]navigator.Result, len(plan.results))
		for index := range plan.results {
			normalized[index] = plan.results[index].result
		}
		plan.response.Selection = findSelection(
			navigator.FindOutcome(plan.response.Outcome),
			normalized,
			plan.resultsTruncated,
		)
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
		path:      result.Path,
		text:      result.Code,
		startLine: line,
		truncated: result.CodeTruncated,
	}
}

func primaryInspectScope(location string, results []navigator.Result) int {
	for index := range results {
		if results[index].Kind != "scope" {
			continue
		}
		lean, ok := leanResultFor(results[index])
		if ok && lean.Location == location {
			return index
		}
	}
	// Navigator guarantees its inspected scope is first. Keep that guarantee
	// useful even when input used an equivalent, non-canonical path spelling.
	if len(results) > 0 && results[0].Kind == "scope" {
		return 0
	}
	return -1
}

func findSelection(
	outcome navigator.FindOutcome,
	results []navigator.Result,
	truncated bool,
) string {
	if outcome != navigator.FindOutcomeSymbol {
		return ""
	}
	definitions := make(map[string]struct{})
	for index := range results {
		line := actionableResultLine(&results[index])
		if results[index].Kind == "def" && results[index].Path != "" && line > 0 {
			definitions[fmt.Sprintf("%s:%d", results[index].Path, line)] = struct{}{}
		}
	}
	switch {
	case len(definitions) > 1:
		return selectionAmbiguous
	case truncated || len(definitions) == 0:
		return selectionIncomplete
	default:
		return selectionUnique
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

// distinctFilesFirst keeps the definition-first/rank order as its source of
// truth, but defers repeated files until every represented file has one slot.
// This spends a small result cap on broader evidence without losing the best
// definition or disturbing order within either pass.
func distinctFilesFirst(results []leanResultSource) []leanResultSource {
	ordered := make([]leanResultSource, 0, len(results))
	repeated := make([]leanResultSource, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for index := range results {
		path := results[index].result.Path
		if _, exists := seen[path]; exists {
			repeated = append(repeated, results[index])
			continue
		}
		seen[path] = struct{}{}
		ordered = append(ordered, results[index])
	}
	return append(ordered, repeated...)
}

func fitLeanResponse(plan *leanResponsePlan, tool string, budget int) bool {
	response := &plan.response
	minimumResults := 0
	if plan.resultsTruncated || len(plan.results) > maximumLeanResults {
		markLeanTruncated(response, "results")
	}
	if plan.sourceTruncated {
		markLeanTruncated(response, "source")
	}
	if !leanFits(response, budget) {
		return false
	}

	// A complete scope is the direct answer to inspect. It is atomic: if the
	// whole block cannot fit, retain the exact inspected location instead.
	evidenceFit := false
	if plan.evidence != nil {
		evidenceFit = fitLeanEvidence(response, plan.evidence, budget)
	}
	if tool == "inspect" && !evidenceFit && plan.inspectFallback != nil {
		if !tryRequiredLeanResult(response, coreLeanResult(plan.inspectFallback.lean), budget) {
			return false
		}
		minimumResults = 1
	}

	resultOffset := len(response.Results)
	selected := make([]int, 0, min(len(plan.results), maximumLeanResults))
	primary := firstActionableResult(tool, plan.results)
	if primary >= 0 {
		candidate := coreLeanResult(plan.results[primary].lean)
		if tool == "find" {
			if !tryRequiredLeanResult(response, candidate, budget) {
				return false
			}
			selected = append(selected, primary)
			minimumResults = 1
		} else if tryLeanResult(response, candidate, budget) {
			selected = append(selected, primary)
		}
	}

	for index := range plan.results {
		if len(response.Results) >= maximumLeanResults || containsIndex(selected, index) {
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
		// Truncation markers are mandatory. Evict optional results first and
		// preserve direct source evidence until nothing else can make the
		// response fit.
		for !leanFits(response, budget) && len(response.Results) > minimumResults {
			response.Results = response.Results[:len(response.Results)-1]
			selected = selected[:len(selected)-1]
		}
		if !leanFits(response, budget) && response.Evidence != nil {
			response.Evidence = nil
			markLeanTruncated(response, "source")
			if tool == "inspect" && plan.inspectFallback != nil {
				if !tryRequiredLeanResult(
					response, coreLeanResult(plan.inspectFallback.lean), budget,
				) {
					return false
				}
				minimumResults = 1
				resultOffset = len(response.Results)
			}
		}
		if !leanFits(response, budget) && minimumResults > 0 {
			response.Results[0].Symbol = ""
		}
	}
	if !leanFits(response, budget) {
		return false
	}

	// Scope and signature are optional enrichment after exact candidates, evidence,
	// and core result identities have fit.
	for selectedIndex, sourceIndex := range selected {
		resultIndex := resultOffset + selectedIndex
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
			// Find and outline are indexes: navigator signatures are definition
			// source lines, so emitting them would duplicate the job of inspect.
			if (tool == "find" || tool == "outline") && field == "signature" {
				continue
			}
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
		result := &results[index].result
		if result.Path != "" && (actionableResultLine(result) > 0 || tool == "find") {
			return index
		}
	}
	return -1
}

func coreLeanResult(result leanResult) leanResult {
	result.Scope = ""
	result.Signature = ""
	return result
}

func tryRequiredLeanResult(
	response *leanResponse,
	result leanResult,
	budget int,
) bool {
	response.Results = append(response.Results, result)
	if !leanFits(response, budget) {
		response.Results[len(response.Results)-1].Symbol = ""
	}
	if !leanFits(response, budget) {
		response.Results = response.Results[:len(response.Results)-1]
		return false
	}
	return true
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

func fitLeanEvidence(response *leanResponse, source *leanEvidenceSource, budget int) bool {
	evidence := &leanEvidence{
		Kind: "scope", Start: evidenceLocation(source.path, source.startLine), Text: source.text,
	}
	response.Evidence = evidence
	if leanFits(response, budget) {
		return true
	}
	response.Evidence = nil
	markLeanTruncated(response, "source")
	return false
}

func evidenceLocation(path string, line int) string {
	return fmt.Sprintf("%s:%d", path, line)
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
