package runstats

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const SchemaVersion = 2

var scopeSifterPattern = regexp.MustCompile(
	`(?:^|[\n\r\t ;|&('"` + "`" + `])(?:[^ \t\n\r;|&=('"` + "`" + `]+/)?` +
		`scopesifter(?:\.bin)?\s+(changed|find|inspect|outline)(?:\s|$)`,
)

var scopeSifterPrefixPattern = regexp.MustCompile(
	`(?:^|[\n\r\t ;|&('"` + "`" + `])(?:[^ \t\n\r;|&=('"` + "`" + `]+/)?` +
		`scopesifter(?:\.bin)?\s+$`,
)

var pathPattern = regexp.MustCompile(
	`(?:[A-Za-z0-9_.@+-]+/)+[A-Za-z0-9_.@+-]+(?::[0-9]+)?`,
)

var knownShellOperations = []string{
	"awk",
	"cat",
	"find",
	"git",
	"go",
	"grep",
	"head",
	"jq",
	"ls",
	"make",
	"perl",
	"python",
	"python3",
	"rg",
	"sed",
	"sha256sum",
	"tail",
	"tar",
	"wc",
}

type Count struct {
	Name             string `json:"name"`
	ToolCalls        int    `json:"tool_calls"`
	Invocations      int    `json:"invocations"`
	OutputCharacters int    `json:"output_characters"`
}

type ScopeSifterInvocation struct {
	Subcommand string `json:"subcommand"`
}

type Call struct {
	ExitCode               *int                    `json:"exit_code,omitempty"`
	OperationInvocations   map[string]int          `json:"operation_invocations"`
	Status                 string                  `json:"status,omitempty"`
	ID                     string                  `json:"id"`
	ToolType               string                  `json:"tool_type"`
	ScopeSifterShapeError  string                  `json:"scopesifter_command_shape_error,omitempty"`
	OutputSHA256           string                  `json:"output_sha256,omitempty"`
	PrimaryOperation       string                  `json:"primary_operation"`
	Command                string                  `json:"command,omitempty"`
	Operations             []string                `json:"operations"`
	ScopeSifterInvocations []ScopeSifterInvocation `json:"scopesifter_invocations,omitempty"`
	Index                  int                     `json:"index"`
	OutputCharacters       int                     `json:"output_characters"`
	CompletedEvent         int                     `json:"completed_event"`
	StartedEvent           int                     `json:"started_event"`
}

type Edge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Kind       string   `json:"kind"`
	Basis      string   `json:"basis"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

type CallGraph struct {
	DependencyModel string `json:"dependency_model"`
	Nodes           []Call `json:"nodes"`
	Edges           []Edge `json:"edges"`
}

type Stats struct {
	CallGraph                    CallGraph `json:"call_graph"`
	ScopeSifterShapeViolations   []string  `json:"scopesifter_command_shape_violations"`
	Calls                        []Call    `json:"calls"`
	Operations                   []Count   `json:"operations"`
	ToolTypes                    []Count   `json:"tool_types"`
	ScopeSifterToolCalls         int       `json:"scopesifter_tool_calls"`
	ScopeSifterInvocations       int       `json:"scopesifter_invocations"`
	OtherToolCalls               int       `json:"other_tool_calls"`
	SchemaVersion                int       `json:"schema_version"`
	TemporalEdgeCount            int       `json:"temporal_edge_count"`
	OutputReferenceEdgeCount     int       `json:"output_reference_edge_count"`
	CommandExecutionToolCalls    int       `json:"command_execution_tool_calls"`
	TotalToolCalls               int       `json:"total_tool_calls"`
	ScopeSifterCommandShapeValid bool      `json:"scopesifter_command_shape_valid"`
}

type event struct {
	Type string          `json:"type"`
	Item json.RawMessage `json:"item"`
}

type item struct {
	ExitCode         *int            `json:"exit_code"`
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Command          string          `json:"command"`
	Status           string          `json:"status"`
	Name             string          `json:"name"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	AggregatedOutput json.RawMessage `json:"aggregated_output"`
	Output           json.RawMessage `json:"output"`
	Result           json.RawMessage `json:"result"`
}

type operationHit struct {
	name  string
	start int
}

type reference struct {
	value string
	kind  string
}

type completedCall struct {
	output string
	call   Call
}

func Analyze(r io.Reader) (Stats, error) {
	decoder := json.NewDecoder(bufio.NewReader(r))
	startedEvents := make(map[string]int)
	startedCommands := make(map[string]item)
	seenCommandIDs := make(map[string]bool)
	completedCalls := make([]completedCall, 0)
	eventIndex := 0

	for {
		var current event
		err := decoder.Decode(&current)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Stats{}, fmt.Errorf("decode JSON event: %w", err)
		}
		eventIndex++
		if len(current.Item) == 0 {
			continue
		}
		var completed item
		if err := json.Unmarshal(current.Item, &completed); err != nil {
			return Stats{}, fmt.Errorf("decode %s item: %w", current.Type, err)
		}
		if current.Type == "item.started" && isToolItem(completed.Type) {
			if completed.ID != "" {
				startedEvents[completed.ID] = eventIndex
			}
			if completed.Type == "command_execution" {
				if completed.ID == "" {
					return Stats{}, fmt.Errorf(
						"command_execution started event %d has no ID",
						eventIndex,
					)
				}
				if completed.Command == "" {
					return Stats{}, fmt.Errorf(
						"command_execution %q started without a command",
						completed.ID,
					)
				}
				if seenCommandIDs[completed.ID] {
					return Stats{}, fmt.Errorf(
						"duplicate command_execution ID %q",
						completed.ID,
					)
				}
				seenCommandIDs[completed.ID] = true
				startedCommands[completed.ID] = completed
			}
			continue
		}
		if current.Type != "item.completed" {
			continue
		}
		if !isToolItem(completed.Type) {
			continue
		}
		if completed.Type == "command_execution" {
			started, ok := startedCommands[completed.ID]
			if !ok {
				return Stats{}, fmt.Errorf(
					"command_execution %q completed without a matching start",
					completed.ID,
				)
			}
			if started.Type != completed.Type || started.Command != completed.Command {
				return Stats{}, fmt.Errorf(
					"command_execution %q changed between start and completion",
					completed.ID,
				)
			}
			if completed.ExitCode == nil || *completed.ExitCode < 0 {
				return Stats{}, fmt.Errorf(
					"command_execution %q has no valid exit code",
					completed.ID,
				)
			}
			if (completed.Status != "completed" || *completed.ExitCode != 0) &&
				(completed.Status != "failed" || *completed.ExitCode == 0) {
				return Stats{}, fmt.Errorf(
					"command_execution %q has inconsistent status %q and exit code %d",
					completed.ID,
					completed.Status,
					*completed.ExitCode,
				)
			}
			delete(startedCommands, completed.ID)
		}

		output := rawValueText(completed.AggregatedOutput)
		if output == "" {
			output = rawValueText(completed.Output)
		}
		if output == "" {
			output = rawValueText(completed.Result)
		}
		operations, invocations, shapeErr := classifyOperations(completed)
		primary := primaryOperation(completed.Type, operations)
		id := completed.ID
		if id == "" {
			id = "tool-" + strconv.Itoa(len(completedCalls)+1)
		}
		startedEvent := startedEvents[completed.ID]
		if startedEvent == 0 {
			startedEvent = eventIndex
		}
		call := Call{
			ID:                     id,
			ToolType:               completed.Type,
			StartedEvent:           startedEvent,
			CompletedEvent:         eventIndex,
			PrimaryOperation:       primary,
			Operations:             operationNames(operations),
			OperationInvocations:   operationInvocationCounts(operations),
			Command:                completed.Command,
			Status:                 completed.Status,
			ExitCode:               completed.ExitCode,
			OutputCharacters:       utf8.RuneCountInString(output),
			ScopeSifterInvocations: invocations,
			ScopeSifterShapeError:  shapeErr,
		}
		if output != "" {
			sum := sha256.Sum256([]byte(output))
			call.OutputSHA256 = hex.EncodeToString(sum[:])
		}
		completedCalls = append(completedCalls, completedCall{
			call:   call,
			output: output,
		})
	}
	if len(startedCommands) > 0 {
		ids := make([]string, 0, len(startedCommands))
		for id := range startedCommands {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return Stats{}, fmt.Errorf(
			"command_execution did not complete: %s",
			strings.Join(ids, ", "),
		)
	}

	sort.SliceStable(completedCalls, func(i, j int) bool {
		if completedCalls[i].call.StartedEvent == completedCalls[j].call.StartedEvent {
			return completedCalls[i].call.CompletedEvent < completedCalls[j].call.CompletedEvent
		}
		return completedCalls[i].call.StartedEvent < completedCalls[j].call.StartedEvent
	})
	calls := make([]Call, 0, len(completedCalls))
	outputs := make([]string, 0, len(completedCalls))
	for index, completed := range completedCalls {
		completed.call.Index = index + 1
		calls = append(calls, completed.call)
		outputs = append(outputs, completed.output)
	}
	return buildStats(calls, outputs), nil
}

func rawValueText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "agent_message", "error", "reasoning", "":
		return false
	case "command_execution", "file_change", "image_generation", "todo_list", "web_search":
		return true
	default:
		return strings.HasSuffix(itemType, "_call") ||
			strings.HasSuffix(itemType, "_tool_call")
	}
}

func classifyOperations(completed item) ([]operationHit, []ScopeSifterInvocation, string) {
	if completed.Type != "command_execution" {
		name := completed.Name
		if name == "" {
			name = completed.Tool
		}
		if completed.Server != "" && name != "" {
			name = completed.Server + "." + name
		}
		if name == "" {
			name = completed.Type
		}
		return []operationHit{{name: name}}, nil, ""
	}

	var hits []operationHit
	var invocations []ScopeSifterInvocation
	for _, match := range scopeSifterPattern.FindAllStringSubmatchIndex(completed.Command, -1) {
		subcommand := completed.Command[match[2]:match[3]]
		name := "scopesifter." + subcommand
		hits = append(hits, operationHit{name: name, start: match[0]})
		invocations = append(invocations, ScopeSifterInvocation{Subcommand: subcommand})
	}
	_, shapeErr := ValidateScopeSifterCommand(completed.Command)

	for _, operation := range knownShellOperations {
		pattern := commandPattern(operation)
		for _, match := range pattern.FindAllStringIndex(completed.Command, -1) {
			matched := completed.Command[match[0]:match[1]]
			nameOffset := strings.LastIndex(matched, operation)
			operationStart := match[0] + nameOffset
			if operation == "find" && isScopeSifterSubcommand(completed.Command, operationStart) {
				continue
			}
			hits = append(hits, operationHit{name: operation, start: operationStart})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].start == hits[j].start {
			return hits[i].name < hits[j].name
		}
		return hits[i].start < hits[j].start
	})
	if len(hits) == 0 {
		hits = append(hits, operationHit{name: "shell"})
	}
	if shapeErr != nil {
		return hits, invocations, shapeErr.Error()
	}
	return hits, invocations, ""
}

// ValidateScopeSifterCommand returns the number of lexically visible scopesifter
// navigation invocations and rejects commands that do not execute exactly one
// standalone invocation. The Codex transcript records a shell command rather
// than an execve trace, so accepting compound or dynamically expanded commands
// would let one visible token stand in for an arbitrary number of executions.
func ValidateScopeSifterCommand(command string) (int, error) {
	matches := scopeSifterPattern.FindAllStringSubmatchIndex(command, -1)
	if len(matches) == 0 {
		return 0, nil
	}
	if len(matches) != 1 {
		return len(matches), fmt.Errorf(
			"scopesifter navigation must be one standalone invocation",
		)
	}
	body, ok := standaloneShellBody(command)
	if !ok {
		return 1, fmt.Errorf(
			"scopesifter navigation is not a standalone shell command",
		)
	}
	bodyMatches := scopeSifterPattern.FindAllStringSubmatchIndex(body, -1)
	if len(bodyMatches) != 1 || bodyMatches[0][0] != 0 {
		return 1, fmt.Errorf(
			"scopesifter navigation is not the executed shell command",
		)
	}
	if strings.Count(body, "scopesifter") != 1 ||
		strings.ContainsAny(body, "\n\r;&|`<>$*?[]{}\\()~#") {
		return 1, fmt.Errorf(
			"scopesifter navigation contains shell composition or expansion",
		)
	}
	jsonPattern := regexp.MustCompile(`(?:^|[ \t])--json(?:[ \t]|$)`)
	if len(jsonPattern.FindAllStringIndex(body, -1)) != 1 {
		return 1, fmt.Errorf(
			"scopesifter navigation must request exactly one JSON response",
		)
	}
	return 1, nil
}

// ValidatedScopeSifterSubcommand returns the subcommand of a safe standalone
// scopesifter navigation command. An empty result means the command contains no
// lexically visible scopesifter navigation invocation.
func ValidatedScopeSifterSubcommand(command string) (string, error) {
	count, err := ValidateScopeSifterCommand(command)
	if err != nil || count == 0 {
		return "", err
	}
	match := scopeSifterPattern.FindStringSubmatch(command)
	if len(match) != 2 {
		return "", fmt.Errorf("scopesifter navigation subcommand is ambiguous")
	}
	return match[1], nil
}

func standaloneShellBody(command string) (string, bool) {
	if strings.HasPrefix(command, "scopesifter ") ||
		strings.HasPrefix(command, "scopesifter.bin ") {
		return command, strings.TrimSpace(command) == command
	}
	for _, prefix := range []string{
		"/usr/bin/zsh -lc ",
		"/bin/zsh -lc ",
		"/usr/bin/bash -lc ",
		"/bin/bash -lc ",
		"/bin/sh -lc ",
		"zsh -lc ",
		"bash -lc ",
		"sh -lc ",
	} {
		if !strings.HasPrefix(command, prefix) {
			continue
		}
		quoted := strings.TrimPrefix(command, prefix)
		if len(quoted) < 2 {
			return "", false
		}
		quote := quoted[0]
		if (quote != '\'' && quote != '"') || quoted[len(quoted)-1] != quote {
			return "", false
		}
		body := quoted[1 : len(quoted)-1]
		return body, strings.TrimSpace(body) == body
	}
	return "", false
}

func isScopeSifterSubcommand(command string, operationStart int) bool {
	prefix := command[:operationStart]
	return scopeSifterPrefixPattern.MatchString(prefix)
}

func commandPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?:^|[\n\r\t ;|&('"` + "`" + `])(?:/[^ \t\n\r;|&('"` + "`" + `]+/)*` +
			regexp.QuoteMeta(name) + `(?:[ \t\n\r;|&)'"]|$)`,
	)
}

func operationInvocationCounts(hits []operationHit) map[string]int {
	counts := make(map[string]int)
	for _, hit := range hits {
		counts[hit.name]++
	}
	return counts
}

func operationNames(hits []operationHit) []string {
	seen := make(map[string]bool)
	var names []string
	for _, hit := range hits {
		if seen[hit.name] {
			continue
		}
		seen[hit.name] = true
		names = append(names, hit.name)
	}
	return names
}

func primaryOperation(toolType string, hits []operationHit) string {
	names := operationNames(hits)
	if toolType != "command_execution" {
		return names[0]
	}
	if len(names) == 1 {
		return names[0]
	}
	scopeSifterNames := 0
	var scopeSifterName string
	for _, name := range names {
		if strings.HasPrefix(name, "scopesifter.") {
			scopeSifterNames++
			scopeSifterName = name
		}
	}
	if scopeSifterNames == len(names) {
		if scopeSifterNames == 1 {
			return scopeSifterName
		}
		return "scopesifter.multiple"
	}
	return "compound-shell"
}

func buildStats(calls []Call, outputs []string) Stats {
	stats := Stats{
		SchemaVersion:                SchemaVersion,
		ScopeSifterCommandShapeValid: true,
		ScopeSifterShapeViolations:   []string{},
		Calls:                        calls,
	}
	toolTypes := make(map[string]*Count)
	operations := make(map[string]*Count)

	for _, call := range calls {
		stats.TotalToolCalls++
		if call.ToolType == "command_execution" {
			stats.CommandExecutionToolCalls++
		}
		if len(call.ScopeSifterInvocations) > 0 {
			stats.ScopeSifterToolCalls++
			stats.ScopeSifterInvocations += len(call.ScopeSifterInvocations)
		}
		if call.ScopeSifterShapeError != "" {
			stats.ScopeSifterCommandShapeValid = false
			stats.ScopeSifterShapeViolations = append(
				stats.ScopeSifterShapeViolations,
				call.Command,
			)
		}

		addCallCount(toolTypes, call.ToolType, 1, call.OutputCharacters)
		seen := make(map[string]bool)
		for _, operation := range call.Operations {
			if !seen[operation] {
				addCallCount(operations, operation, 0, call.OutputCharacters)
				seen[operation] = true
			}
		}
		for operation, invocations := range call.OperationInvocations {
			operations[operation].Invocations += invocations
		}
	}
	stats.OtherToolCalls = stats.TotalToolCalls - stats.ScopeSifterToolCalls
	stats.ToolTypes = sortedCounts(toolTypes)
	stats.Operations = sortedCounts(operations)

	edges := buildEdges(calls, outputs)
	for _, edge := range edges {
		switch edge.Kind {
		case "next_tool_call":
			stats.TemporalEdgeCount++
		case "output_reference":
			stats.OutputReferenceEdgeCount++
		}
	}
	stats.CallGraph = CallGraph{
		DependencyModel: "The transcript has no explicit tool-result dependency IDs. next_tool_call edges are temporal/model-context inferences; output_reference edges additionally prove literal reuse of a prior output value in a later command, but causal use remains inferred.",
		Nodes:           calls,
		Edges:           edges,
	}
	return stats
}

func addCallCount(counts map[string]*Count, name string, invocations, outputCharacters int) {
	count := counts[name]
	if count == nil {
		count = &Count{Name: name}
		counts[name] = count
	}
	count.ToolCalls++
	count.Invocations += invocations
	count.OutputCharacters += outputCharacters
}

func sortedCounts(counts map[string]*Count) []Count {
	result := make([]Count, 0, len(counts))
	for _, count := range counts {
		result = append(result, *count)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ToolCalls == result[j].ToolCalls {
			return result[i].Name < result[j].Name
		}
		return result[i].ToolCalls > result[j].ToolCalls
	})
	return result
}

func buildEdges(calls []Call, outputs []string) []Edge {
	edges := make([]Edge, 0)
	for targetIndex, target := range calls {
		sourceIndex := latestCompletedBefore(calls, targetIndex, target.StartedEvent)
		if sourceIndex >= 0 {
			edges = append(edges, Edge{
				From:       calls[sourceIndex].ID,
				To:         target.ID,
				Kind:       "next_tool_call",
				Basis:      "this was the latest tool result completed before the model issued the target call",
				Confidence: "inferred",
			})
		}
	}

	referencesByCall := make([][]reference, len(calls))
	for index, output := range outputs {
		referencesByCall[index] = extractReferences(output)
	}
	for _, call := range calls {
		if call.Command != "" {
			lastReference := make(map[string]int)
			referenceKind := make(map[string]string)
			for sourceIndex, source := range calls {
				if source.CompletedEvent >= call.StartedEvent {
					continue
				}
				for _, ref := range referencesByCall[sourceIndex] {
					previousIndex, exists := lastReference[ref.value]
					if !exists || calls[previousIndex].CompletedEvent < source.CompletedEvent {
						lastReference[ref.value] = sourceIndex
						referenceKind[ref.value] = ref.kind
					}
				}
			}
			evidenceBySource := make(map[int][]string)
			for value, sourceIndex := range lastReference {
				if !commandContainsReference(call.Command, value) {
					continue
				}
				evidence := referenceKind[value] + ":" + value
				evidenceBySource[sourceIndex] = append(evidenceBySource[sourceIndex], evidence)
			}
			sources := make([]int, 0, len(evidenceBySource))
			for source := range evidenceBySource {
				sources = append(sources, source)
			}
			sort.Ints(sources)
			for _, source := range sources {
				evidence := evidenceBySource[source]
				sort.Strings(evidence)
				if len(evidence) > 8 {
					evidence = evidence[:8]
				}
				edges = append(edges, Edge{
					From:       calls[source].ID,
					To:         call.ID,
					Kind:       "output_reference",
					Basis:      "later command literally contains a path, location, or symbol emitted by the earlier result",
					Confidence: "inferred-literal-match",
					Evidence:   evidence,
				})
			}
		}
	}
	return edges
}

func latestCompletedBefore(calls []Call, targetIndex, startedEvent int) int {
	sourceIndex := -1
	completedEvent := -1
	for index, call := range calls {
		if index == targetIndex || call.CompletedEvent >= startedEvent {
			continue
		}
		if call.CompletedEvent > completedEvent {
			sourceIndex = index
			completedEvent = call.CompletedEvent
		}
	}
	return sourceIndex
}

func extractReferences(output string) []reference {
	if output == "" {
		return nil
	}
	found := make(map[string]string)
	var value any
	if json.Unmarshal([]byte(output), &value) == nil {
		collectJSONReferences(value, "", found)
	}
	for _, path := range pathPattern.FindAllString(output, -1) {
		addReference(found, path, "path")
	}

	result := make([]reference, 0, len(found))
	for value, kind := range found {
		result = append(result, reference{value: value, kind: kind})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].kind == result[j].kind {
			return result[i].value < result[j].value
		}
		return result[i].kind < result[j].kind
	})
	if len(result) > 2000 {
		result = result[:2000]
	}
	return result
}

func collectJSONReferences(value any, key string, found map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			collectJSONReferences(child, childKey, found)
		}
	case []any:
		for _, child := range typed {
			collectJSONReferences(child, key, found)
		}
	case string:
		switch key {
		case "path", "file", "location":
			addReference(found, typed, "path")
		case "symbol", "scope":
			addReference(found, typed, "symbol")
		}
	}
}

func addReference(found map[string]string, value, kind string) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 300 {
		return
	}
	if kind == "symbol" && len(value) < 4 {
		return
	}
	found[value] = kind
	if kind == "path" {
		if colon := strings.LastIndex(value, ":"); colon > 0 {
			if _, err := strconv.Atoi(value[colon+1:]); err == nil {
				found[value[:colon]] = "path"
			}
		}
	}
}

func commandContainsReference(command, value string) bool {
	if strings.Contains(value, "/") || strings.Contains(value, ":") {
		return strings.Contains(command, value)
	}
	pattern := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(value) + `(?:$|[^A-Za-z0-9_])`)
	return pattern.MatchString(command)
}

func WriteDOT(w io.Writer, stats Stats) error {
	if _, err := fmt.Fprintln(w, "digraph tool_calls {"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, `  rankdir="LR";`); err != nil {
		return err
	}
	for _, call := range stats.CallGraph.Nodes {
		label := fmt.Sprintf("%d. %s", call.Index, call.PrimaryOperation)
		if _, err := fmt.Fprintf(
			w,
			"  %s [label=%s];\n",
			dotQuote(call.ID),
			dotQuote(label),
		); err != nil {
			return err
		}
	}
	for _, edge := range stats.CallGraph.Edges {
		style := "dashed"
		label := "next"
		if edge.Kind == "output_reference" {
			style = "solid"
			label = "output ref"
		}
		if _, err := fmt.Fprintf(
			w,
			"  %s -> %s [label=%s, style=%s];\n",
			dotQuote(edge.From),
			dotQuote(edge.To),
			dotQuote(label),
			dotQuote(style),
		); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, "}")
	return err
}

func WriteMarkdown(w io.Writer, stats Stats) error {
	if _, err := fmt.Fprintln(w, "# Tool Call Graph"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n%s\n\n", stats.CallGraph.DependencyModel); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "## Nodes"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| # | ID | Tool type | Primary operation | Operations | Output characters |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| ---: | --- | --- | --- | --- | ---: |"); err != nil {
		return err
	}
	for _, call := range stats.CallGraph.Nodes {
		if _, err := fmt.Fprintf(
			w,
			"| %d | `%s` | `%s` | `%s` | %s | %d |\n",
			call.Index,
			markdownCell(call.ID),
			markdownCell(call.ToolType),
			markdownCell(call.PrimaryOperation),
			markdownCell(strings.Join(call.Operations, ", ")),
			call.OutputCharacters,
		); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "\n## Edges"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| From | To | Kind | Confidence | Evidence |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- | --- | --- | --- |"); err != nil {
		return err
	}
	for _, edge := range stats.CallGraph.Edges {
		if _, err := fmt.Fprintf(
			w,
			"| `%s` | `%s` | `%s` | `%s` | %s |\n",
			markdownCell(edge.From),
			markdownCell(edge.To),
			markdownCell(edge.Kind),
			markdownCell(edge.Confidence),
			markdownCell(strings.Join(edge.Evidence, ", ")),
		); err != nil {
			return err
		}
	}
	return nil
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(value, "\n", " ")
}

func dotQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
