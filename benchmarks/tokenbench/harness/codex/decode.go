package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
)

const (
	maxJSONLBytes  = 16 << 20
	maxJSONLLine   = 1 << 20
	maxJSONLEvents = 100000
)

type execUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

type eventHeader struct {
	Type string `json:"type"`
}

type threadStartedEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
}

type turnStartedEvent struct {
	Type string `json:"type"`
}

type turnCompletedEvent struct {
	Usage *execUsage `json:"usage"`
	Type  string     `json:"type"`
}

type threadError struct {
	Message string `json:"message"`
}

type turnFailedEvent struct {
	Error *threadError `json:"error"`
	Type  string       `json:"type"`
}

type threadErrorEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type itemEvent struct {
	Type string          `json:"type"`
	Item json.RawMessage `json:"item"`
}

type itemHeader struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type textItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

type commandItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int32 `json:"exit_code"`
	Status           string `json:"status"`
}

type mcpItemResult struct {
	Content           []json.RawMessage `json:"content"`
	Meta              json.RawMessage   `json:"_meta,omitempty"`
	StructuredContent json.RawMessage   `json:"structured_content"`
}

type mcpItemError struct {
	Message string `json:"message"`
}

type mcpItem struct {
	Result    *mcpItemResult  `json:"result"`
	Error     *mcpItemError   `json:"error"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Status    string          `json:"status"`
	Arguments json.RawMessage `json:"arguments"`
}

type itemState struct {
	typeName  string
	toolKind  string
	command   string
	server    string
	tool      string
	arguments string
	started   bool
	completed bool
}

type execCall struct {
	kind            string
	name            string
	argumentsSHA256 string
}

type execOutput struct {
	typeName      string
	kind          string
	name          string
	payloadSHA256 string
}

type execDecoded struct {
	finalAnswer string
	toolCalls   []execCall
	outputs     []execOutput
	usage       harness.Usage
}

type execDecoder struct {
	items       map[string]itemState
	finalAnswer string
	toolCalls   []execCall
	outputs     []execOutput
	usage       harness.Usage
	eventCount  int
	phase       int
}

// Decode implements harness.Adapter. It requires successful bounded process
// capture, an exact v0.144.0 JSONL lifecycle, and both strict runner artifacts.
// Provider model, usage, and tool claims must agree with Codex JSONL before an
// observation is returned.
func (adapter *Adapter) Decode(
	ctx context.Context,
	execution harness.RawExecution,
) (harness.Observation, error) {
	if err := contextError(ctx); err != nil {
		return harness.Observation{}, err
	}
	if err := adapter.validateConfigured(); err != nil {
		return harness.Observation{}, err
	}
	switch {
	case execution.LaunchFailed:
		return harness.Observation{}, errors.New("codex execution could not be launched")
	case execution.TimedOut:
		return harness.Observation{}, errors.New("codex execution timed out")
	case execution.Cancelled:
		return harness.Observation{}, errors.New("codex execution was cancelled")
	case execution.StdoutTruncated:
		return harness.Observation{}, errors.New("codex stdout was truncated")
	case execution.StderrTruncated:
		return harness.Observation{}, errors.New("codex stderr was truncated")
	case execution.ExitCode != 0:
		return harness.Observation{}, fmt.Errorf("codex execution exited with status %d", execution.ExitCode)
	case len(execution.Stderr) != 0:
		return harness.Observation{}, errors.New("codex execution wrote to stderr")
	case len(execution.Stdout) == 0:
		return harness.Observation{}, errors.New("codex execution wrote no JSONL")
	case len(execution.Stdout) > maxJSONLBytes:
		return harness.Observation{}, errors.New("codex JSONL exceeds its byte limit")
	case !utf8.Valid(execution.Stdout):
		return harness.Observation{}, errors.New("codex JSONL must be valid UTF-8")
	case execution.Stdout[len(execution.Stdout)-1] != '\n':
		return harness.Observation{}, errors.New("codex JSONL must end with a newline")
	}

	trace, err := decodeArtifacts(execution.Artifacts)
	if err != nil {
		return harness.Observation{}, err
	}
	execOutput, err := decodeExecJSONL(ctx, execution.Stdout)
	if err != nil {
		return harness.Observation{}, err
	}
	if !harness.EqualUsageNativeComponents(execOutput.usage, trace.usage) {
		return harness.Observation{}, fmt.Errorf(
			"codex JSONL usage %+v differs from provider usage %+v",
			execOutput.usage, trace.usage,
		)
	}
	if len(execOutput.outputs) != len(trace.outputs) {
		return harness.Observation{}, fmt.Errorf(
			"codex JSONL reported %d mapped output items but provider trace reported %d",
			len(execOutput.outputs), len(trace.outputs),
		)
	}
	for index, observed := range execOutput.outputs {
		claimed := trace.outputs[index]
		if observed.typeName != claimed.Type || observed.kind != claimed.Kind ||
			observed.name != claimed.Name || observed.payloadSHA256 != claimed.PayloadSHA256 {
			return harness.Observation{}, fmt.Errorf(
				"codex output item %d differs between JSONL and provider trace",
				index,
			)
		}
	}
	return harness.Observation{
		FinalAnswer: execOutput.finalAnswer,
		Model:       trace.model,
		ToolCalls: append(
			make([]string, 0, len(trace.toolCallNames)),
			trace.toolCallNames...,
		),
		Usage:     harness.CloneUsage(trace.usage),
		Completed: true,
	}, nil
}

func decodeExecJSONL(ctx context.Context, raw []byte) (execDecoded, error) {
	decoder := execDecoder{
		items:     make(map[string]itemState),
		toolCalls: make([]execCall, 0),
		outputs:   make([]execOutput, 0),
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), maxJSONLLine)
	for scanner.Scan() {
		if err := contextError(ctx); err != nil {
			return execDecoded{}, err
		}
		line := scanner.Bytes()
		decoder.eventCount++
		if decoder.eventCount > maxJSONLEvents {
			return execDecoded{}, errors.New("codex JSONL exceeds its event limit")
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return execDecoded{}, fmt.Errorf("codex JSONL event %d is blank", decoder.eventCount)
		}
		if err := decoder.consume(line); err != nil {
			return execDecoded{}, fmt.Errorf("codex JSONL event %d: %w", decoder.eventCount, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return execDecoded{}, fmt.Errorf("scan Codex JSONL: %w", err)
	}
	if decoder.phase != 3 {
		return execDecoded{}, errors.New("codex JSONL did not complete its turn")
	}
	if decoder.finalAnswer == "" {
		return execDecoded{}, errors.New("codex JSONL omitted a nonempty final answer")
	}
	for id, item := range decoder.items {
		if item.started && !item.completed {
			return execDecoded{}, fmt.Errorf("codex JSONL item %q did not complete", id)
		}
	}
	if err := harness.ValidateUsage(decoder.usage); err != nil {
		return execDecoded{}, fmt.Errorf("codex JSONL usage: %w", err)
	}
	return execDecoded{
		finalAnswer: decoder.finalAnswer,
		usage:       decoder.usage,
		toolCalls:   append([]execCall(nil), decoder.toolCalls...),
		outputs:     append([]execOutput(nil), decoder.outputs...),
	}, nil
}

func (decoder *execDecoder) consume(line []byte) error {
	if err := rejectDuplicateJSONKeys(line); err != nil {
		return err
	}
	var header eventHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return err
	}
	if header.Type == "" {
		return errors.New("event type is required")
	}
	if decoder.phase == 3 {
		return errors.New("event appeared after turn.completed")
	}
	switch header.Type {
	case "thread.started":
		if decoder.eventCount != 1 || decoder.phase != 0 {
			return errors.New("thread.started must be the first and only thread event")
		}
		var event threadStartedEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		if !validText(event.ThreadID) || event.ThreadID == "" {
			return errors.New("thread.started has an invalid thread_id")
		}
		decoder.phase = 1
		return nil
	case "turn.started":
		if decoder.eventCount != 2 || decoder.phase != 1 {
			return errors.New("turn.started must immediately follow thread.started")
		}
		var event turnStartedEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		decoder.phase = 2
		return nil
	case "turn.completed":
		if decoder.phase != 2 {
			return errors.New("turn.completed appeared outside the active turn")
		}
		for id, item := range decoder.items {
			if item.started && !item.completed {
				return fmt.Errorf("item %q remained active at turn.completed", id)
			}
		}
		var event turnCompletedEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		if event.Usage == nil {
			return errors.New("turn.completed omitted usage")
		}
		decoder.usage = harness.Usage{
			InputTokens:       event.Usage.InputTokens,
			CachedInputTokens: event.Usage.CachedInputTokens,
			OutputTokens:      event.Usage.OutputTokens,
			ReasoningTokens:   event.Usage.ReasoningOutputTokens,
		}
		if err := harness.ValidateUsage(decoder.usage); err != nil {
			return err
		}
		decoder.phase = 3
		return nil
	case "turn.failed":
		var event turnFailedEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		if event.Error == nil || !validText(event.Error.Message) || event.Error.Message == "" {
			return errors.New("turn.failed omitted a valid error")
		}
		return errors.New("codex turn.failed")
	case "error":
		var event threadErrorEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		if !validText(event.Message) || event.Message == "" {
			return errors.New("error event omitted a valid message")
		}
		return errors.New("codex emitted a fatal error event")
	case "item.started", "item.updated", "item.completed":
		if decoder.phase != 2 {
			return errors.New("item event appeared outside the active turn")
		}
		var event itemEvent
		if err := strictUnmarshalJSON(line, &event); err != nil {
			return err
		}
		if len(event.Item) == 0 || bytes.Equal(bytes.TrimSpace(event.Item), []byte("null")) {
			return errors.New("item event omitted item")
		}
		return decoder.consumeItem(header.Type, event.Item)
	default:
		return fmt.Errorf("unsupported Codex event type %q", header.Type)
	}
}

func (decoder *execDecoder) consumeItem(eventType string, raw json.RawMessage) error {
	var header itemHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	if !validText(header.ID) || header.ID == "" {
		return errors.New("item id is invalid")
	}
	if header.Type == "" {
		return errors.New("item type is required")
	}
	state, exists := decoder.items[header.ID]
	switch eventType {
	case "item.started":
		if exists {
			return fmt.Errorf("item %q started more than once", header.ID)
		}
		parsed, err := parseItem(raw, header.Type, "in_progress")
		if err != nil {
			return err
		}
		parsed.started = true
		decoder.items[header.ID] = parsed
		if parsed.typeName == "command_execution" {
			argumentsSHA256 := digest([]byte(parsed.arguments))
			decoder.toolCalls = append(decoder.toolCalls, execCall{
				kind:            ToolKindCommand,
				name:            "exec_command",
				argumentsSHA256: argumentsSHA256,
			})
			decoder.outputs = append(decoder.outputs, execOutput{
				typeName:      OutputTypeToolCall,
				kind:          ToolKindCommand,
				name:          "exec_command",
				payloadSHA256: argumentsSHA256,
			})
		}
		if parsed.typeName == "mcp_tool_call" {
			name := parsed.tool
			if parsed.toolKind == ToolKindMCP {
				name = parsed.server + "." + parsed.tool
			}
			argumentsSHA256 := digest([]byte(parsed.arguments))
			decoder.toolCalls = append(decoder.toolCalls, execCall{
				kind:            parsed.toolKind,
				name:            name,
				argumentsSHA256: argumentsSHA256,
			})
			decoder.outputs = append(decoder.outputs, execOutput{
				typeName:      OutputTypeToolCall,
				kind:          parsed.toolKind,
				name:          name,
				payloadSHA256: argumentsSHA256,
			})
		}
		return nil
	case "item.updated":
		if !exists || !state.started || state.completed {
			return fmt.Errorf("item %q updated outside its active lifecycle", header.ID)
		}
		parsed, err := parseItem(raw, header.Type, "in_progress")
		if err != nil {
			return err
		}
		if err := sameItemIdentity(state, parsed); err != nil {
			return fmt.Errorf("item %q update: %w", header.ID, err)
		}
		return nil
	case "item.completed":
		parsed, err := parseItem(raw, header.Type, "terminal")
		if err != nil {
			return err
		}
		if parsed.typeName == "command_execution" || parsed.typeName == "mcp_tool_call" {
			if !exists || !state.started || state.completed {
				return fmt.Errorf("item %q completed outside its active lifecycle", header.ID)
			}
			if err := sameItemIdentity(state, parsed); err != nil {
				return fmt.Errorf("item %q completion: %w", header.ID, err)
			}
		} else if exists {
			if !state.started || state.completed {
				return fmt.Errorf("item %q completed more than once", header.ID)
			}
			if err := sameItemIdentity(state, parsed); err != nil {
				return fmt.Errorf("item %q completion: %w", header.ID, err)
			}
		}
		parsed.started = parsed.started || exists
		parsed.completed = true
		decoder.items[header.ID] = parsed
		if parsed.typeName == "agent_message" {
			var item textItem
			if err := strictUnmarshalJSON(raw, &item); err != nil {
				return err
			}
			if !validText(item.Text) {
				return errors.New("agent message contains invalid text")
			}
			decoder.finalAnswer = item.Text
			decoder.outputs = append(decoder.outputs, execOutput{
				typeName:      OutputTypeAssistantMessage,
				payloadSHA256: digest([]byte(item.Text)),
			})
		}
		if parsed.typeName == "reasoning" {
			var item textItem
			if err := strictUnmarshalJSON(raw, &item); err != nil {
				return err
			}
			if strings.TrimSpace(item.Text) != "" {
				decoder.outputs = append(decoder.outputs, execOutput{
					typeName:      OutputTypeReasoning,
					payloadSHA256: digest([]byte(item.Text)),
				})
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported item event %q", eventType)
	}
}

func parseItem(raw json.RawMessage, typeName, phase string) (itemState, error) {
	state := itemState{typeName: typeName}
	switch typeName {
	case "agent_message", "reasoning":
		var item textItem
		if err := strictUnmarshalJSON(raw, &item); err != nil {
			return itemState{}, err
		}
		if !validText(item.Text) {
			return itemState{}, fmt.Errorf("%s text contains invalid text", typeName)
		}
		return state, nil
	case "command_execution":
		var item commandItem
		if err := strictUnmarshalJSON(raw, &item); err != nil {
			return itemState{}, err
		}
		if !validText(item.Command) || item.Command == "" || !validText(item.AggregatedOutput) {
			return itemState{}, errors.New("command execution contains invalid text")
		}
		if phase == "in_progress" {
			if item.Status != "in_progress" || item.ExitCode != nil {
				return itemState{}, errors.New("active command execution has a terminal state")
			}
		} else if item.Status != "completed" && item.Status != "failed" && item.Status != "declined" {
			return itemState{}, fmt.Errorf("command execution has invalid terminal status %q", item.Status)
		}
		state.command = item.Command
		arguments, err := execCommandArguments(item.Command)
		if err != nil {
			return itemState{}, err
		}
		state.arguments = string(arguments)
		return state, nil
	case "mcp_tool_call":
		var item mcpItem
		if err := strictUnmarshalJSON(raw, &item); err != nil {
			return itemState{}, err
		}
		if len(item.Arguments) == 0 {
			return itemState{}, errors.New("mcp tool call omitted arguments")
		}
		toolKind, err := validateMCPItemIdentity(item)
		if err != nil {
			return itemState{}, err
		}
		arguments, err := canonicalJSON(item.Arguments)
		if err != nil {
			return itemState{}, fmt.Errorf("canonicalize MCP arguments: %w", err)
		}
		if phase == "in_progress" {
			if item.Status != "in_progress" || item.Result != nil || item.Error != nil {
				return itemState{}, errors.New("active MCP tool call has a terminal state")
			}
		} else {
			switch item.Status {
			case "completed":
				if item.Result == nil || item.Error != nil || item.Result.Content == nil {
					return itemState{}, errors.New("completed MCP tool call has an invalid result")
				}
				structured := bytes.TrimSpace(item.Result.StructuredContent)
				if toolKind == ToolKindMCPSupport {
					if !bytes.Equal(structured, []byte("null")) {
						return itemState{}, errors.New("completed MCP support call has unexpected structured content")
					}
				} else if len(structured) == 0 || bytes.Equal(structured, []byte("null")) {
					return itemState{}, errors.New("completed scopesifter call omitted structured content")
				}
			case "failed":
				if item.Result != nil || item.Error == nil ||
					!validText(item.Error.Message) || item.Error.Message == "" {
					return itemState{}, errors.New("failed MCP tool call has an invalid error")
				}
			default:
				return itemState{}, fmt.Errorf("mcp tool call has invalid terminal status %q", item.Status)
			}
		}
		state.toolKind = toolKind
		state.server = item.Server
		state.tool = item.Tool
		state.arguments = string(arguments)
		return state, nil
	case "file_change", "collab_tool_call", "web_search", "todo_list", "error":
		return itemState{}, fmt.Errorf("disabled Codex item type %q appeared", typeName)
	default:
		return itemState{}, fmt.Errorf("unsupported Codex item type %q", typeName)
	}
}

func validateMCPItemIdentity(item mcpItem) (string, error) {
	if _, ok := allowedNormalizedMCPCalls[item.Server+"."+item.Tool]; ok {
		if item.Server != "scopesifter" {
			return "", fmt.Errorf("unsupported MCP server %q in Codex JSONL", item.Server)
		}
		return ToolKindMCP, nil
	}
	if _, ok := allowedMCPSupportCalls[item.Tool]; !ok {
		return "", fmt.Errorf("unsupported MCP tool %q in Codex JSONL", item.Tool)
	}
	arguments, err := mcpArgumentObject(item.Arguments)
	if err != nil {
		return "", err
	}
	switch item.Tool {
	case "list_mcp_resources", "list_mcp_resource_templates":
		if err := exactRawKeys(arguments, "server", "cursor"); err != nil {
			return "", err
		}
		server := ""
		if raw, exists := arguments["server"]; exists {
			server, err = requiredRawString(raw, "server")
			if err != nil || server != "scopesifter" {
				return "", errors.New("mcp support list call has an invalid server argument")
			}
		}
		if raw, exists := arguments["cursor"]; exists {
			if _, err := requiredRawString(raw, "cursor"); err != nil {
				return "", err
			}
		}
		wantedEventServer := "codex"
		if server != "" {
			wantedEventServer = server
		}
		if item.Server != wantedEventServer {
			return "", errors.New("mcp support list event server disagrees with its arguments")
		}
	case "read_mcp_resource":
		if err := exactRawKeys(arguments, "server", "uri"); err != nil {
			return "", err
		}
		serverRaw, serverExists := arguments["server"]
		uriRaw, uriExists := arguments["uri"]
		if !serverExists || !uriExists {
			return "", errors.New("read_mcp_resource omitted server or uri")
		}
		server, err := requiredRawString(serverRaw, "server")
		if err != nil || server != "scopesifter" || item.Server != "scopesifter" {
			return "", errors.New("read_mcp_resource has an invalid server")
		}
		if _, err := requiredRawString(uriRaw, "uri"); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported MCP support tool %q", item.Tool)
	}
	return ToolKindMCPSupport, nil
}

func mcpArgumentObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := strictUnmarshalJSON(raw, &object); err != nil {
		return nil, fmt.Errorf("decode MCP support arguments: %w", err)
	}
	if object == nil {
		return nil, errors.New("mcp support arguments must be an object")
	}
	return object, nil
}

func exactRawKeys(object map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range object {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("unsupported MCP support argument %q", key)
		}
	}
	return nil
}

func requiredRawString(raw json.RawMessage, name string) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("mcp support argument %q must be nonempty text", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || !validText(value) {
		return "", fmt.Errorf("mcp support argument %q must be nonempty text", name)
	}
	return value, nil
}

// execCommandArguments inverts the exact Unix display transformation used by
// Codex v0.144.0: Shell::derive_exec_args(cmd, false), followed by
// shlex 1.3.0 try_join. The adapter pins allow_login_shell=false and accepts no
// optional provider exec arguments, so the third argv entry is the complete
// and only JSONL-recoverable provider argument.
func execCommandArguments(command string) ([]byte, error) {
	argv, err := splitShlex(command)
	if err != nil {
		return nil, fmt.Errorf("decode Codex command display: %w", err)
	}
	if len(argv) != 3 || argv[1] != "-c" {
		return nil, errors.New("codex command display is not the pinned non-login shell form")
	}
	if !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] ||
		!validText(argv[0]) || argv[2] == "" || !validText(argv[2]) {
		return nil, errors.New("codex command display contains an invalid shell or command")
	}
	joined, err := joinShlex(argv)
	if err != nil || joined != command {
		return nil, errors.New("codex command display is not canonical shlex 1.3.0 output")
	}
	arguments, err := json.Marshal(map[string]any{"cmd": argv[2]})
	if err != nil {
		return nil, fmt.Errorf("encode canonical exec_command arguments: %w", err)
	}
	return arguments, nil
}

func splitShlex(value string) ([]string, error) {
	const (
		shlexUnquoted = iota
		shlexSingleQuoted
		shlexDoubleQuoted
	)
	state := shlexUnquoted
	started := false
	var word strings.Builder
	words := make([]string, 0, 3)
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch state {
		case shlexUnquoted:
			switch character {
			case ' ':
				if !started {
					return nil, errors.New("noncanonical shell word separator")
				}
				words = append(words, word.String())
				word.Reset()
				started = false
			case '\'':
				state = shlexSingleQuoted
				started = true
			case '"':
				state = shlexDoubleQuoted
				started = true
			case '\\':
				return nil, errors.New("noncanonical unquoted shell escape")
			default:
				word.WriteByte(character)
				started = true
			}
		case shlexSingleQuoted:
			if character == '\'' {
				state = shlexUnquoted
			} else {
				word.WriteByte(character)
			}
		case shlexDoubleQuoted:
			switch character {
			case '"':
				state = shlexUnquoted
			case '\\':
				index++
				if index >= len(value) || !strings.ContainsRune("$`\"\\", rune(value[index])) {
					return nil, errors.New("invalid double-quoted shell escape")
				}
				word.WriteByte(value[index])
			default:
				word.WriteByte(character)
			}
		}
	}
	if state != shlexUnquoted || !started {
		return nil, errors.New("unterminated or empty shell word")
	}
	words = append(words, word.String())
	return words, nil
}

func joinShlex(words []string) (string, error) {
	quoted := make([]string, len(words))
	for index, word := range words {
		value, err := quoteShlex(word)
		if err != nil {
			return "", err
		}
		quoted[index] = value
	}
	return strings.Join(quoted, " "), nil
}

func quoteShlex(value string) (string, error) {
	if value == "" {
		return "''", nil
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("shell word contains NUL")
	}
	const (
		quoteUnquoted = 1 << iota
		quoteSingle
		quoteDouble
	)
	var result strings.Builder
	for offset := 0; offset < len(value); {
		allowed := byte(quoteUnquoted | quoteSingle | quoteDouble)
		index := offset
		if value[offset] == '^' {
			allowed = quoteSingle
			index++
		}
		for index < len(value) {
			character := value[index]
			next := allowed
			if character >= 0x80 {
				next &^= quoteUnquoted
			} else {
				if !shlexUnquotedOK(character) {
					next &^= quoteUnquoted
				}
				if character == '\'' || character == '^' || character == '\\' {
					next &^= quoteSingle
				}
				if character == '`' || character == '$' || character == '!' || character == '^' {
					next &^= quoteDouble
				}
			}
			if next == 0 {
				break
			}
			allowed = next
			index++
		}
		if index == offset {
			return "", errors.New("cannot quote shell word")
		}
		chunk := value[offset:index]
		switch {
		case allowed&quoteUnquoted != 0:
			result.WriteString(chunk)
		case allowed&quoteSingle != 0:
			result.WriteByte('\'')
			result.WriteString(chunk)
			result.WriteByte('\'')
		case allowed&quoteDouble != 0:
			result.WriteByte('"')
			for chunkIndex := range len(chunk) {
				character := chunk[chunkIndex]
				if character == '$' || character == '`' || character == '"' || character == '\\' {
					result.WriteByte('\\')
				}
				result.WriteByte(character)
			}
			result.WriteByte('"')
		default:
			return "", errors.New("cannot select shell quoting strategy")
		}
		offset = index
	}
	return result.String(), nil
}

func shlexUnquotedOK(character byte) bool {
	return character >= '0' && character <= '9' ||
		character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		strings.ContainsRune("+-./:@]_", rune(character))
}

func sameItemIdentity(previous, next itemState) error {
	if previous.typeName != next.typeName {
		return fmt.Errorf("type changed from %q to %q", previous.typeName, next.typeName)
	}
	if previous.toolKind != next.toolKind || previous.command != next.command || previous.server != next.server ||
		previous.tool != next.tool || previous.arguments != next.arguments {
		return errors.New("immutable item fields changed")
	}
	return nil
}
