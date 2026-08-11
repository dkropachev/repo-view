package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
	genericrunner "github.com/dkropachev/repo-view/benchmarks/tokenbench/runner"
	"github.com/dkropachev/repo-view/internal/repoviewmcp"
)

var normalizedMCPNames = map[string]string{
	"mcp__repo_view__changed": "repo_view.changed",
	"mcp__repo_view__find":    "repo_view.find",
	"mcp__repo_view__inspect": "repo_view.inspect",
	"mcp__repo_view__outline": "repo_view.outline",
	"repo_view.changed":       "repo_view.changed",
	"repo_view.find":          "repo_view.find",
	"repo_view.inspect":       "repo_view.inspect",
	"repo_view.outline":       "repo_view.outline",
}

func parseResponsesRequest(
	raw []byte,
	execution genericrunner.ExecutionRequest,
) (harnesscodex.ResponsesRequestTrace, []harnesscodex.ResponsesToolDeclaration, error) {
	request, err := decodeJSONObject(raw)
	if err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, fmt.Errorf("decode Responses request: %w", err)
	}
	model, err := requiredString(request, "model")
	if err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}
	if model != execution.Invocation.Model {
		return harnesscodex.ResponsesRequestTrace{}, nil, fmt.Errorf(
			"responses request model %q differs from approved model %q",
			model,
			execution.Invocation.Model,
		)
	}
	tools, err := requiredArray(request, "tools")
	if err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}
	declarations := make([]harnesscodex.ResponsesToolDeclaration, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	commandCount := 0
	mcpNames := make(map[string]struct{})
	mcpSupportNames := make(map[string]struct{})
	for index, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok || tool == nil {
			return harnesscodex.ResponsesRequestTrace{}, nil, fmt.Errorf("responses tool %d is not an object", index)
		}
		declaration, err := parseToolDeclaration(tool)
		if err != nil {
			return harnesscodex.ResponsesRequestTrace{}, nil, fmt.Errorf("responses tool %d: %w", index, err)
		}
		key := declaration.Kind + "\x00" + declaration.Name
		if _, duplicate := seen[key]; duplicate {
			return harnesscodex.ResponsesRequestTrace{}, nil, fmt.Errorf(
				"duplicate Responses tool declaration %q",
				declaration.Name,
			)
		}
		seen[key] = struct{}{}
		switch declaration.Kind {
		case harnesscodex.ToolKindMCP:
			mcpNames[declaration.Name] = struct{}{}
		case harnesscodex.ToolKindMCPSupport:
			mcpSupportNames[declaration.Name] = struct{}{}
		default:
			commandCount++
		}
		declarations = append(declarations, declaration)
	}
	if commandCount == 0 {
		return harnesscodex.ResponsesRequestTrace{}, nil, errors.New("responses request omitted the command tool surface")
	}
	if err := validateArmMCPDeclarations(execution.Arm, mcpNames, mcpSupportNames); err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}

	exactBodyDigest := bytesDigest(raw)
	dynamicFields, err := captureRequestDynamicFields(request)
	if err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}
	delete(request, "tools")
	if err := normalizeRequestMetadata(request, execution); err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}
	nonToolDigest, err := canonicalJSONDigest(request)
	if err != nil {
		return harnesscodex.ResponsesRequestTrace{}, nil, err
	}
	trace := harnesscodex.ResponsesRequestTrace{
		Model:                               model,
		ExactBodySHA256:                     exactBodyDigest,
		NonceNormalizedNonToolPayloadSHA256: nonToolDigest,
		DynamicFields:                       cloneDynamicFields(dynamicFields),
		Tools:                               append([]harnesscodex.ResponsesToolDeclaration(nil), declarations...),
	}
	return trace, declarations, nil
}

func parseToolDeclaration(tool map[string]any) (harnesscodex.ResponsesToolDeclaration, error) {
	typeName, err := requiredString(tool, "type")
	if err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	name, err := requiredString(tool, "name")
	if err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	description, err := requiredString(tool, "description")
	if err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	var schema any
	var strictValue *bool
	switch typeName {
	case "function":
		if err := exactKeys(tool, "type", "name", "description", "parameters", "strict"); err != nil {
			return harnesscodex.ResponsesToolDeclaration{}, err
		}
		parameters, err := requiredObject(tool, "parameters")
		if err != nil {
			return harnesscodex.ResponsesToolDeclaration{}, err
		}
		strict, present := tool["strict"]
		strictBool, ok := strict.(bool)
		if !present || !ok {
			return harnesscodex.ResponsesToolDeclaration{}, errors.New("function tool strict must be present and boolean")
		}
		strictValue = &strictBool
		schema = parameters
	case "custom":
		if err := exactKeys(tool, "type", "name", "description", "format"); err != nil {
			return harnesscodex.ResponsesToolDeclaration{}, err
		}
		format, err := requiredObject(tool, "format")
		if err != nil {
			return harnesscodex.ResponsesToolDeclaration{}, err
		}
		schema = format
	default:
		return harnesscodex.ResponsesToolDeclaration{}, fmt.Errorf("unsupported Responses tool type %q", typeName)
	}

	kind, normalizedName, err := normalizeToolName(name)
	if err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	if kind == harnesscodex.ToolKindMCPSupport {
		if typeName != "function" || strictValue == nil || *strictValue {
			return harnesscodex.ResponsesToolDeclaration{}, errors.New(
				"codex MCP support tools must use the pinned non-strict function shape",
			)
		}
	}
	schemaDigest, err := canonicalJSONDigest(schema)
	if err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	declaration := harnesscodex.ResponsesToolDeclaration{
		Kind:              kind,
		Name:              normalizedName,
		WireType:          typeName,
		Strict:            strictValue,
		DescriptionSHA256: bytesDigest([]byte(description)),
		InputSchemaSHA256: schemaDigest,
	}
	if err := validatePinnedTreatmentDeclaration(declaration); err != nil {
		return harnesscodex.ResponsesToolDeclaration{}, err
	}
	return declaration, nil
}

func validatePinnedTreatmentDeclaration(
	declaration harnesscodex.ResponsesToolDeclaration,
) error {
	if declaration.Kind != harnesscodex.ToolKindMCP &&
		declaration.Kind != harnesscodex.ToolKindMCPSupport {
		return nil
	}
	if declaration.WireType != "function" || declaration.Strict == nil || *declaration.Strict {
		return fmt.Errorf("treatment tool %q has an unsupported wire shape", declaration.Name)
	}
	wanted, err := pinnedTreatmentDeclarations()
	if err != nil {
		return err
	}
	expected, ok := wanted[declaration.Name]
	if !ok || declaration.DescriptionSHA256 != expected.DescriptionSHA256 ||
		declaration.InputSchemaSHA256 != expected.InputSchemaSHA256 {
		return fmt.Errorf("treatment tool %q differs from its code-owned declaration", declaration.Name)
	}
	return nil
}

func pinnedTreatmentDeclarations() (map[string]harnesscodex.ResponsesToolDeclaration, error) {
	result := make(map[string]harnesscodex.ResponsesToolDeclaration, 7)
	for _, spec := range pinnedTreatmentSpecs() {
		schema, err := providerVisibleTreatmentSchema(spec)
		if err != nil {
			return nil, err
		}
		digest, err := canonicalJSONDigest(schema)
		if err != nil {
			return nil, err
		}
		result[spec.name] = harnesscodex.ResponsesToolDeclaration{
			DescriptionSHA256: bytesDigest([]byte(spec.description)),
			InputSchemaSHA256: digest,
		}
	}
	return result, nil
}

func providerVisibleTreatmentSchema(spec pinnedTreatmentSpec) (map[string]any, error) {
	if spec.kind != harnesscodex.ToolKindMCP {
		return spec.schema, nil
	}
	normalized, err := codexVisibleSchema(spec.schema)
	if err != nil {
		return nil, fmt.Errorf("normalize repo_view.%s provider schema: %w", spec.name, err)
	}
	raw, err := canonicalJSON(normalized)
	if err != nil {
		return nil, err
	}
	// v0.144.0 begins lossy large-schema compaction above 5000 normalized
	// bytes. The four reviewed repo-view schemas must remain below that branch;
	// crossing it requires pinning the official compaction passes too.
	if len(raw) > 5000 {
		return nil, fmt.Errorf("repo-view provider schema %q requires unsupported Codex compaction", spec.name)
	}
	return normalized, nil
}

// codexVisibleSchema mirrors the JsonSchema fields retained by Codex v0.144.0
// after it parses an MCP input schema and serializes that typed subset onto the
// Responses wire. Repo-view's local validation-only bounds/defaults are
// intentionally absent from the provider declaration.
func codexVisibleSchema(schema map[string]any) (map[string]any, error) {
	result := make(map[string]any)
	for key, value := range schema {
		switch key {
		case "$ref", "type", "description", "encrypted", "enum", "required":
			result[key] = value
		case "items":
			child, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("schema items is not an object")
			}
			normalized, err := codexVisibleSchema(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		case "properties", "$defs", "definitions":
			children, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("schema %s is not an object", key)
			}
			normalizedChildren := make(map[string]any, len(children))
			for name, rawChild := range children {
				child, ok := rawChild.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("schema %s child %q is not an object", key, name)
				}
				normalized, err := codexVisibleSchema(child)
				if err != nil {
					return nil, err
				}
				normalizedChildren[name] = normalized
			}
			result[key] = normalizedChildren
		case "additionalProperties":
			if boolean, ok := value.(bool); ok {
				result[key] = boolean
				continue
			}
			child, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("schema additionalProperties is invalid")
			}
			normalized, err := codexVisibleSchema(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		case "anyOf", "oneOf", "allOf":
			children, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("schema %s is not an array", key)
			}
			normalizedChildren := make([]any, len(children))
			for index, rawChild := range children {
				child, ok := rawChild.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("schema %s child %d is not an object", key, index)
				}
				normalized, err := codexVisibleSchema(child)
				if err != nil {
					return nil, err
				}
				normalizedChildren[index] = normalized
			}
			result[key] = normalizedChildren
		}
	}
	return result, nil
}

type pinnedTreatmentSpec struct {
	schema      map[string]any
	kind        string
	name        string
	description string
}

func pinnedTreatmentSpecs() []pinnedTreatmentSpec {
	result := make([]pinnedTreatmentSpec, 0, 7)
	for _, spec := range repoviewmcp.ToolSpecifications() {
		result = append(result, pinnedTreatmentSpec{
			kind:        harnesscodex.ToolKindMCP,
			name:        "repo_view." + spec.Name,
			description: spec.Description,
			schema:      spec.InputSchema,
		})
	}
	return append(result, []pinnedTreatmentSpec{
		{
			kind:        harnesscodex.ToolKindMCPSupport,
			name:        "list_mcp_resources",
			description: "Lists resources provided by MCP servers. Resources allow servers to share data that provides context to language models, such as files, database schemas, or application-specific information. Prefer resources over web search when possible.",
			schema: mcpListSchema(
				"MCP server name. Omit to list resources from every configured server.",
				"Opaque cursor from a previous list_mcp_resources call; omit for the first page.",
			),
		},
		{
			kind:        harnesscodex.ToolKindMCPSupport,
			name:        "list_mcp_resource_templates",
			description: "Lists resource templates provided by MCP servers. Parameterized resource templates allow servers to share data that takes parameters and provides context to language models, such as files, database schemas, or application-specific information. Prefer resource templates over web search when possible.",
			schema: mcpListSchema(
				"MCP server name. Omit to list resource templates from every configured server.",
				"Opaque cursor from a previous list_mcp_resource_templates call; omit for the first page.",
			),
		},
		{
			kind:        harnesscodex.ToolKindMCPSupport,
			name:        "read_mcp_resource",
			description: "Read a specific resource from an MCP server given the server name and resource URI.",
			schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"server": map[string]any{
						"type":        "string",
						"description": "MCP server name exactly as configured. Must match the 'server' field returned by list_mcp_resources.",
					},
					"uri": map[string]any{
						"type":        "string",
						"description": "Resource URI to read. Must be one of the URIs returned by list_mcp_resources.",
					},
				},
				"required": []string{"server", "uri"},
			},
		},
	}...)
}

func mcpListSchema(serverDescription, cursorDescription string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"server": map[string]any{
				"type":        "string",
				"description": serverDescription,
			},
			"cursor": map[string]any{
				"type":        "string",
				"description": cursorDescription,
			},
		},
	}
}

func normalizeToolName(name string) (string, string, error) {
	if normalized, ok := normalizedMCPNames[name]; ok {
		return harnesscodex.ToolKindMCP, normalized, nil
	}
	for _, support := range harnesscodex.AllowedMCPSupportTools() {
		if name == support {
			return harnesscodex.ToolKindMCPSupport, name, nil
		}
	}
	if strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, "repo_view.") {
		return "", "", fmt.Errorf("unsupported MCP tool name %q", name)
	}
	if !validToolName(name) {
		return "", "", fmt.Errorf("invalid command tool name %q", name)
	}
	return harnesscodex.ToolKindCommand, name, nil
}

func validToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validateArmMCPDeclarations(
	arm genericrunner.Arm,
	names map[string]struct{},
	supportNames map[string]struct{},
) error {
	if arm == genericrunner.BaselineArm {
		if len(names) != 0 || len(supportNames) != 0 {
			return errors.New("baseline Responses request exposed an MCP tool surface")
		}
		return nil
	}
	if arm != genericrunner.CandidateArm {
		return fmt.Errorf("unsupported Responses request arm %q", arm)
	}
	wanted := harnesscodex.AllowedMCPTools()
	if len(names) != len(wanted) {
		return fmt.Errorf("candidate Responses request exposed %d MCP tools, want %d", len(names), len(wanted))
	}
	for _, name := range wanted {
		if _, ok := names["repo_view."+name]; !ok {
			return fmt.Errorf("candidate Responses request omitted repo_view.%s", name)
		}
	}
	supportWanted := harnesscodex.AllowedMCPSupportTools()
	if len(supportNames) != len(supportWanted) {
		return fmt.Errorf(
			"candidate Responses request exposed %d MCP support tools, want %d",
			len(supportNames),
			len(supportWanted),
		)
	}
	for _, name := range supportWanted {
		if _, ok := supportNames[name]; !ok {
			return fmt.Errorf("candidate Responses request omitted MCP support tool %s", name)
		}
	}
	return nil
}

func captureRequestDynamicFields(
	request map[string]any,
) ([]harnesscodex.ProviderRequestDynamicFieldTrace, error) {
	client, err := requiredObject(request, "client_metadata")
	if err != nil {
		return nil, err
	}
	metadataRaw, err := requiredString(client, "x-codex-turn-metadata")
	if err != nil {
		return nil, err
	}
	metadata, err := decodeJSONObject([]byte(metadataRaw))
	if err != nil {
		return nil, fmt.Errorf("decode x-codex-turn-metadata for exact commitments: %w", err)
	}
	type fieldSpec struct {
		path           string
		value          any
		classification string
	}
	specs := make([]fieldSpec, 0, 16)
	appendField := func(path string, object map[string]any, key, classification string) error {
		value, present := object[key]
		if !present {
			return fmt.Errorf("dynamic provider field %s is absent", path)
		}
		specs = append(specs, fieldSpec{path, value, classification})
		return nil
	}
	if err := appendField(
		"/prompt_cache_key", request, "prompt_cache_key",
		harnesscodex.DynamicFieldProviderCacheRouting,
	); err != nil {
		return nil, err
	}
	for _, field := range []struct{ key, path string }{
		{"x-codex-installation-id", "/client_metadata/x-codex-installation-id"},
		{"session_id", "/client_metadata/session_id"},
		{"thread_id", "/client_metadata/thread_id"},
		{"x-codex-window-id", "/client_metadata/x-codex-window-id"},
		{"turn_id", "/client_metadata/turn_id"},
	} {
		if err := appendField(field.path, client, field.key, harnesscodex.DynamicFieldFreshProcessNonce); err != nil {
			return nil, err
		}
	}
	for _, field := range []struct{ key, path, classification string }{
		{"installation_id", "/client_metadata/x-codex-turn-metadata/installation_id", harnesscodex.DynamicFieldFreshProcessNonce},
		{"session_id", "/client_metadata/x-codex-turn-metadata/session_id", harnesscodex.DynamicFieldFreshProcessNonce},
		{"thread_id", "/client_metadata/x-codex-turn-metadata/thread_id", harnesscodex.DynamicFieldFreshProcessNonce},
		{"turn_id", "/client_metadata/x-codex-turn-metadata/turn_id", harnesscodex.DynamicFieldFreshProcessNonce},
		{"window_id", "/client_metadata/x-codex-turn-metadata/window_id", harnesscodex.DynamicFieldFreshProcessNonce},
		{"turn_started_at_unix_ms", "/client_metadata/x-codex-turn-metadata/turn_started_at_unix_ms", harnesscodex.DynamicFieldClockNonce},
	} {
		if err := appendField(field.path, metadata, field.key, field.classification); err != nil {
			return nil, err
		}
	}
	input, err := requiredArray(request, "input")
	if err != nil {
		return nil, err
	}
	for index, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("responses input item %d is not an object", index)
		}
		rawPassthrough, present := item["internal_chat_message_metadata_passthrough"]
		if !present {
			continue
		}
		passthrough, ok := rawPassthrough.(map[string]any)
		if !ok || passthrough == nil {
			return nil, fmt.Errorf("responses input item %d turn metadata is not an object", index)
		}
		if err := appendField(
			fmt.Sprintf("/input/%d/internal_chat_message_metadata_passthrough/turn_id", index),
			passthrough,
			"turn_id",
			harnesscodex.DynamicFieldFreshProcessNonce,
		); err != nil {
			return nil, err
		}
	}
	fields := make([]harnesscodex.ProviderRequestDynamicFieldTrace, len(specs))
	for index, spec := range specs {
		digest, err := canonicalJSONDigest(spec.value)
		if err != nil {
			return nil, fmt.Errorf("commit dynamic provider field %s: %w", spec.path, err)
		}
		fields[index] = harnesscodex.ProviderRequestDynamicFieldTrace{
			JSONPointer:    spec.path,
			Present:        true,
			SHA256:         digest,
			Classification: spec.classification,
		}
	}
	sort.Slice(fields, func(left, right int) bool {
		return fields[left].JSONPointer < fields[right].JSONPointer
	})
	return fields, nil
}

func cloneDynamicFields(
	fields []harnesscodex.ProviderRequestDynamicFieldTrace,
) []harnesscodex.ProviderRequestDynamicFieldTrace {
	return append(
		make([]harnesscodex.ProviderRequestDynamicFieldTrace, 0, len(fields)),
		fields...,
	)
}

// normalizeRequestMetadata validates the exact dynamic identity envelope
// emitted by Codex v0.144.0 for a root exec turn, then replaces only those
// fresh-process identifiers with a schema marker before parity hashing.
func normalizeRequestMetadata(
	request map[string]any,
	execution genericrunner.ExecutionRequest,
) error {
	promptCacheKey, err := requiredString(request, "prompt_cache_key")
	if err != nil {
		return err
	}
	client, err := requiredObject(request, "client_metadata")
	if err != nil {
		return err
	}
	if err := exactKeys(
		client,
		"x-codex-installation-id",
		"session_id",
		"thread_id",
		"x-codex-window-id",
		"turn_id",
		"x-codex-turn-metadata",
	); err != nil {
		return fmt.Errorf("codex client_metadata: %w", err)
	}
	installationID, err := requiredString(client, "x-codex-installation-id")
	if err != nil {
		return err
	}
	sessionID, err := requiredString(client, "session_id")
	if err != nil {
		return err
	}
	threadID, err := requiredString(client, "thread_id")
	if err != nil {
		return err
	}
	windowID, err := requiredString(client, "x-codex-window-id")
	if err != nil {
		return err
	}
	turnID, err := requiredString(client, "turn_id")
	if err != nil {
		return err
	}
	metadataJSON, err := requiredString(client, "x-codex-turn-metadata")
	if err != nil {
		return err
	}
	if !validCanonicalUUID(installationID, '4') || !validCanonicalUUID(threadID, '7') ||
		!validCanonicalUUID(turnID, '7') {
		return errors.New("codex client_metadata contains a noncanonical request identifier")
	}
	if sessionID != threadID || promptCacheKey != threadID || windowID != threadID+":0" {
		return errors.New("codex request identity fields are internally inconsistent")
	}

	metadata, err := decodeJSONObject([]byte(metadataJSON))
	if err != nil {
		return fmt.Errorf("decode x-codex-turn-metadata: %w", err)
	}
	if err := exactKeys(
		metadata,
		"installation_id",
		"session_id",
		"thread_id",
		"turn_id",
		"window_id",
		"request_kind",
		"thread_source",
		"sandbox",
		"turn_started_at_unix_ms",
		"workspaces",
	); err != nil {
		return fmt.Errorf("x-codex-turn-metadata: %w", err)
	}
	wantedStrings := map[string]string{
		"installation_id": installationID,
		"session_id":      sessionID,
		"thread_id":       threadID,
		"turn_id":         turnID,
		"window_id":       windowID,
		"request_kind":    "turn",
		"thread_source":   "user",
		"sandbox":         "seccomp",
	}
	for key, wanted := range wantedStrings {
		observed, err := requiredString(metadata, key)
		if err != nil {
			return err
		}
		if observed != wanted {
			return fmt.Errorf("x-codex-turn-metadata field %q is inconsistent", key)
		}
	}
	started, err := nonnegativeInteger(metadata, "turn_started_at_unix_ms")
	if err != nil || started == 0 {
		if err == nil {
			err = errors.New("timestamp must be positive")
		}
		return fmt.Errorf("x-codex-turn-metadata turn_started_at_unix_ms: %w", err)
	}
	workspaces, hasWorkspaces := metadata["workspaces"]
	if hasWorkspaces {
		if err := validateWorkspaceMetadata(workspaces, execution.Invocation); err != nil {
			return err
		}
	}

	const normalizedInstallationID = "00000000-0000-4000-8000-000000000000"
	const normalizedThreadID = "00000000-0000-7000-8000-000000000000"
	const normalizedTurnID = "00000000-0000-7000-8000-000000000001"
	if err := normalizeInputTurnMetadata(request, turnID, normalizedTurnID); err != nil {
		return err
	}
	normalizedWindowID := normalizedThreadID + ":0"
	normalizedMetadata := map[string]any{
		"installation_id":         normalizedInstallationID,
		"session_id":              normalizedThreadID,
		"thread_id":               normalizedThreadID,
		"turn_id":                 normalizedTurnID,
		"window_id":               normalizedWindowID,
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "seccomp",
		"turn_started_at_unix_ms": json.Number("0"),
	}
	// Workspace enrichment is provider-visible and is not a fresh-process
	// identifier. Preserve its exact validated object (including optional-field
	// presence and remotes) so the non-tool commitment detects any arm drift.
	if hasWorkspaces {
		normalizedMetadata["workspaces"] = workspaces
	}
	normalizedMetadataJSON, err := canonicalJSON(normalizedMetadata)
	if err != nil {
		return err
	}
	request["prompt_cache_key"] = normalizedThreadID
	request["client_metadata"] = map[string]any{
		"x-codex-installation-id": normalizedInstallationID,
		"session_id":              normalizedThreadID,
		"thread_id":               normalizedThreadID,
		"x-codex-window-id":       normalizedWindowID,
		"turn_id":                 normalizedTurnID,
		"x-codex-turn-metadata":   string(normalizedMetadataJSON),
	}
	return nil
}

func normalizeInputTurnMetadata(request map[string]any, turnID, normalizedTurnID string) error {
	input, err := requiredArray(request, "input")
	if err != nil {
		return err
	}
	found := 0
	for index, value := range input {
		item, ok := value.(map[string]any)
		if !ok || item == nil {
			return fmt.Errorf("responses input item %d is not an object", index)
		}
		value, exists := item["internal_chat_message_metadata_passthrough"]
		if !exists {
			continue
		}
		passthrough, ok := value.(map[string]any)
		if !ok || passthrough == nil {
			return fmt.Errorf("responses input item %d turn metadata is not an object", index)
		}
		if err := exactKeys(passthrough, "turn_id"); err != nil {
			return fmt.Errorf("responses input item %d turn metadata: %w", index, err)
		}
		observed, err := requiredString(passthrough, "turn_id")
		if err != nil || observed != turnID {
			return fmt.Errorf("responses input item %d turn ID is inconsistent", index)
		}
		item["internal_chat_message_metadata_passthrough"] = map[string]any{
			"turn_id": normalizedTurnID,
		}
		found++
	}
	if found == 0 {
		return errors.New("responses input omitted Codex turn metadata passthrough")
	}
	return nil
}

func validateWorkspaceMetadata(value any, invocation harness.Invocation) error {
	workspaces, ok := value.(map[string]any)
	if !ok || len(workspaces) != 1 {
		return errors.New("x-codex-turn-metadata workspaces must contain exactly the approved source")
	}
	entryValue, ok := workspaces[invocation.WorkingDirectory]
	if !ok {
		return errors.New("x-codex-turn-metadata workspace path differs from the approved source")
	}
	entry, ok := entryValue.(map[string]any)
	if !ok || entry == nil {
		return errors.New("x-codex-turn-metadata workspace must be an object")
	}
	if err := exactKeys(entry, "associated_remote_urls", "latest_git_commit_hash", "has_changes"); err != nil {
		return fmt.Errorf("x-codex-turn-metadata workspace: %w", err)
	}
	if value, exists := entry["latest_git_commit_hash"]; exists {
		commit, ok := value.(string)
		if !ok || commit != invocation.SourceRevision {
			return errors.New("x-codex-turn-metadata workspace commit differs from the approved revision")
		}
	}
	if value, exists := entry["has_changes"]; exists {
		changed, ok := value.(bool)
		if !ok || changed {
			return errors.New("x-codex-turn-metadata workspace is not clean")
		}
	}
	if value, exists := entry["associated_remote_urls"]; exists {
		remotes, ok := value.(map[string]any)
		if !ok || len(remotes) > 64 {
			return errors.New("x-codex-turn-metadata workspace remotes are invalid")
		}
		for name, rawURL := range remotes {
			remoteURL, ok := rawURL.(string)
			if !ok || name == "" || !validText(name) || remoteURL == "" || !validText(remoteURL) {
				return errors.New("x-codex-turn-metadata workspace remote is invalid")
			}
		}
	}
	return nil
}

func validCanonicalUUID(value string, version byte) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' ||
		value[18] != '-' || value[23] != '-' || value[14] != version ||
		!strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func parseResponsesBody(
	body []byte,
	mediaType string,
	request harnesscodex.ResponsesRequestTrace,
	expectedProvider string,
	declarations []harnesscodex.ResponsesToolDeclaration,
	maxEventBytes int,
	maxEvents int,
) (harnesscodex.ResponsesResponseTrace, error) {
	var response map[string]any
	var events []harnesscodex.ResponsesSSEEventTrace
	var completedSequence *int
	var err error
	switch mediaType {
	case "application/json":
		response, err = decodeJSONObject(body)
		events = []harnesscodex.ResponsesSSEEventTrace{}
	case "text/event-stream":
		response, events, completedSequence, err = completedSSEResponse(
			body, maxEventBytes, maxEvents,
		)
	default:
		return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf("unsupported upstream media type %q", mediaType)
	}
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	canonicalResponseSHA256, err := canonicalJSONDigest(response)
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	parsed, err := parseCompletedResponse(response, request, expectedProvider, declarations)
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	parsed.Wire = harnesscodex.ProviderResponseWireTrace{
		StatusCode:              200,
		MediaType:               mediaType,
		BodyBytes:               len(body),
		ExactBodySHA256:         bytesDigest(body),
		CanonicalResponseSHA256: canonicalResponseSHA256,
		SSEEvents:               events,
		CompletedEventSequence:  completedSequence,
	}
	return parsed, nil
}

func parseCompletedResponse(
	response map[string]any,
	request harnesscodex.ResponsesRequestTrace,
	expectedProvider string,
	declarations []harnesscodex.ResponsesToolDeclaration,
) (harnesscodex.ResponsesResponseTrace, error) {
	model, err := requiredString(response, "model")
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	if model != expectedProvider {
		return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf(
			"provider response model %q differs from pinned model %q",
			model,
			expectedProvider,
		)
	}
	usageObject, err := requiredObject(response, "usage")
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	usage, providerTotalTokens, err := parseUsage(usageObject)
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	output, err := requiredArray(response, "output")
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	declared := make(map[string]struct{}, len(declarations))
	for _, declaration := range declarations {
		declared[declaration.Kind+"\x00"+declaration.Name] = struct{}{}
	}
	outputsTrace := make([]harnesscodex.ResponsesOutputTrace, 0, len(output))
	for index, value := range output {
		item, ok := value.(map[string]any)
		if !ok || item == nil {
			return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf("provider output item %d is not an object", index)
		}
		typeName, err := requiredString(item, "type")
		if err != nil {
			return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf("provider output item %d: %w", index, err)
		}
		outputTrace, err := parseOutputItem(typeName, item)
		if err != nil {
			return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf("provider output item %d: %w", index, err)
		}
		if outputTrace.Type == harnesscodex.OutputTypeToolCall {
			if _, ok := declared[outputTrace.Kind+"\x00"+outputTrace.Name]; !ok {
				return harnesscodex.ResponsesResponseTrace{}, fmt.Errorf(
					"provider called undeclared tool %q",
					outputTrace.Name,
				)
			}
		}
		outputsTrace = append(outputsTrace, outputTrace)
	}
	requestToolsSHA256, err := canonicalJSONDigest(declarations)
	if err != nil {
		return harnesscodex.ResponsesResponseTrace{}, err
	}
	// Responses trace v3 already has a dedicated provider_total_tokens field.
	// Keep the nested v3 usage object unchanged; Decode restores the optional
	// total after validating and aggregating the explicit provider evidence.
	traceUsage := harnesscodex.ResponsesUsageTrace{
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningTokens:       usage.ReasoningTokens,
	}
	return harnesscodex.ResponsesResponseTrace{
		RequestModel:           request.Model,
		RequestExactBodySHA256: request.ExactBodySHA256,
		RequestNonceNormalizedNonToolPayloadSHA256: request.NonceNormalizedNonToolPayloadSHA256,
		RequestHeadersSHA256:                       request.Headers.ReviewedSemanticSHA256,
		RequestToolsSHA256:                         requestToolsSHA256,
		ResponseModel:                              model,
		Outputs:                                    outputsTrace,
		Usage:                                      &traceUsage,
		ProviderTotalTokens:                        providerTotalTokens,
	}, nil
}

func parseOutputItem(
	typeName string,
	item map[string]any,
) (harnesscodex.ResponsesOutputTrace, error) {
	wireSHA256, err := canonicalJSONDigest(item)
	if err != nil {
		return harnesscodex.ResponsesOutputTrace{}, err
	}
	result := harnesscodex.ResponsesOutputTrace{WireSHA256: wireSHA256}
	switch typeName {
	case "message":
		payloadSHA256, err := parseAssistantMessage(item)
		if err != nil {
			return harnesscodex.ResponsesOutputTrace{}, err
		}
		result.Type = harnesscodex.OutputTypeAssistantMessage
		result.PayloadSHA256 = payloadSHA256
		return result, nil
	case "reasoning":
		payloadSHA256, err := parseReasoningSummary(item)
		if err != nil {
			return harnesscodex.ResponsesOutputTrace{}, err
		}
		result.Type = harnesscodex.OutputTypeReasoning
		result.PayloadSHA256 = payloadSHA256
		return result, nil
	case "function_call":
		call, err := parseOutputCall(item)
		if err != nil {
			return harnesscodex.ResponsesOutputTrace{}, err
		}
		result.Type = harnesscodex.OutputTypeToolCall
		result.PayloadSHA256 = call.ArgumentsSHA256
		result.Kind = call.Kind
		result.Name = call.Name
		return result, nil
	case "custom_tool_call", "local_shell_call", "shell_call":
		return harnesscodex.ResponsesOutputTrace{}, fmt.Errorf(
			"provider used disabled tool-call type %q",
			typeName,
		)
	default:
		return harnesscodex.ResponsesOutputTrace{}, fmt.Errorf(
			"provider output type %q has no reviewed Codex JSONL mapping",
			typeName,
		)
	}
}

func parseAssistantMessage(item map[string]any) (string, error) {
	role, err := requiredString(item, "role")
	if err != nil {
		return "", err
	}
	if role != "assistant" {
		return "", fmt.Errorf("provider message has unsupported role %q", role)
	}
	content, err := requiredArray(item, "content")
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", errors.New("provider assistant message has empty content")
	}
	var text strings.Builder
	for index, value := range content {
		part, ok := value.(map[string]any)
		if !ok || part == nil {
			return "", fmt.Errorf("provider assistant content %d is not an object", index)
		}
		typeName, err := requiredString(part, "type")
		if err != nil {
			return "", fmt.Errorf("provider assistant content %d: %w", index, err)
		}
		if typeName != "input_text" && typeName != "output_text" {
			return "", fmt.Errorf("provider assistant content %d has unsupported type %q", index, typeName)
		}
		value, exists := part["text"]
		partText, ok := value.(string)
		if !exists || !ok || !validText(partText) {
			return "", fmt.Errorf("provider assistant content %d has invalid text", index)
		}
		text.WriteString(partText)
	}
	if text.Len() == 0 {
		return "", errors.New("provider assistant message maps to empty JSONL text")
	}
	if phase, exists := item["phase"]; exists && phase != nil {
		phaseText, ok := phase.(string)
		if !ok || phaseText != "commentary" && phaseText != "final_answer" {
			return "", errors.New("provider assistant message has invalid phase")
		}
	}
	return bytesDigest([]byte(text.String())), nil
}

func parseReasoningSummary(item map[string]any) (string, error) {
	summary, err := requiredArray(item, "summary")
	if err != nil {
		return "", err
	}
	parts := make([]string, len(summary))
	for index, value := range summary {
		part, ok := value.(map[string]any)
		if !ok || part == nil {
			return "", fmt.Errorf("provider reasoning summary %d is not an object", index)
		}
		typeName, err := requiredString(part, "type")
		if err != nil || typeName != "summary_text" {
			return "", fmt.Errorf("provider reasoning summary %d has an invalid type", index)
		}
		value, exists := part["text"]
		text, ok := value.(string)
		if !exists || !ok || !validText(text) {
			return "", fmt.Errorf("provider reasoning summary %d has invalid text", index)
		}
		parts[index] = text
	}
	joined := strings.Join(parts, "\n")
	if strings.TrimSpace(joined) == "" {
		// v0.144.0 deliberately suppresses empty-summary reasoning items from
		// exec JSONL. WireSHA256 still commits the complete encrypted item.
		return "", nil
	}
	return bytesDigest([]byte(joined)), nil
}

func parseOutputCall(
	item map[string]any,
) (harnesscodex.ResponsesToolCall, error) {
	if _, err := requiredString(item, "call_id"); err != nil {
		return harnesscodex.ResponsesToolCall{}, err
	}
	name, err := requiredString(item, "name")
	if err != nil {
		return harnesscodex.ResponsesToolCall{}, err
	}
	if namespace, exists := item["namespace"]; exists && namespace != nil {
		return harnesscodex.ResponsesToolCall{}, errors.New("provider used an unsupported tool namespace")
	}
	kind, normalized, err := normalizeToolName(name)
	if err != nil {
		return harnesscodex.ResponsesToolCall{}, err
	}
	arguments, err := requiredString(item, "arguments")
	if err != nil {
		return harnesscodex.ResponsesToolCall{}, err
	}
	argumentsObject, err := decodeJSONObject([]byte(arguments))
	if err != nil {
		return harnesscodex.ResponsesToolCall{}, fmt.Errorf("decode provider tool arguments: %w", err)
	}
	if kind == harnesscodex.ToolKindCommand {
		if normalized != "exec_command" {
			return harnesscodex.ResponsesToolCall{}, fmt.Errorf(
				"command tool %q has no reviewed Codex JSONL argument mapping",
				normalized,
			)
		}
		// Exec JSONL drops workdir/TTY/yield/output/approval options. Requiring
		// the sole cmd field makes the provider arguments exactly recoverable
		// from the resolved shell argv instead of pretending dropped fields
		// were reconciled.
		if err := exactKeys(argumentsObject, "cmd"); err != nil {
			return harnesscodex.ResponsesToolCall{}, fmt.Errorf(
				"exec_command arguments are not exactly JSONL-recoverable: %w",
				err,
			)
		}
		if _, err := requiredString(argumentsObject, "cmd"); err != nil {
			return harnesscodex.ResponsesToolCall{}, err
		}
	}
	argumentsSHA256, err := canonicalJSONDigest(argumentsObject)
	if err != nil {
		return harnesscodex.ResponsesToolCall{}, err
	}
	return harnesscodex.ResponsesToolCall{
		Kind:            kind,
		Name:            normalized,
		ArgumentsSHA256: argumentsSHA256,
	}, nil
}

func parseUsage(object map[string]any) (harness.Usage, *int64, error) {
	if err := exactKeys(
		object,
		"input_tokens",
		"input_tokens_details",
		"output_tokens",
		"output_tokens_details",
		"total_tokens",
	); err != nil {
		return harness.Usage{}, nil, err
	}
	input, err := nonnegativeInteger(object, "input_tokens")
	if err != nil {
		return harness.Usage{}, nil, err
	}
	output, err := nonnegativeInteger(object, "output_tokens")
	if err != nil {
		return harness.Usage{}, nil, err
	}
	cached := int64(0)
	if detailsValue, ok := object["input_tokens_details"]; ok {
		details, ok := detailsValue.(map[string]any)
		if !ok {
			return harness.Usage{}, nil, errors.New("input_tokens_details must be an object")
		}
		if err := exactKeys(details, "cached_tokens"); err != nil {
			return harness.Usage{}, nil, err
		}
		cached, err = nonnegativeInteger(details, "cached_tokens")
		if err != nil {
			return harness.Usage{}, nil, err
		}
	}
	reasoning := int64(0)
	if detailsValue, ok := object["output_tokens_details"]; ok {
		details, ok := detailsValue.(map[string]any)
		if !ok {
			return harness.Usage{}, nil, errors.New("output_tokens_details must be an object")
		}
		if err := exactKeys(details, "reasoning_tokens"); err != nil {
			return harness.Usage{}, nil, err
		}
		reasoning, err = nonnegativeInteger(details, "reasoning_tokens")
		if err != nil {
			return harness.Usage{}, nil, err
		}
	}
	var providerTotalTokens *int64
	if total, present, err := optionalNonnegativeInteger(object, "total_tokens"); err != nil {
		return harness.Usage{}, nil, err
	} else if present && (input > math.MaxInt64-output || total != input+output) {
		return harness.Usage{}, nil, errors.New("provider total_tokens does not equal input_tokens plus output_tokens")
	} else if present {
		providerTotalTokens = new(int64)
		*providerTotalTokens = total
	}
	var usageProviderTotal *int64
	if providerTotalTokens != nil {
		total := *providerTotalTokens
		usageProviderTotal = &total
	}
	usage := harness.Usage{
		InputTokens:         input,
		CachedInputTokens:   cached,
		OutputTokens:        output,
		ReasoningTokens:     reasoning,
		ProviderTotalTokens: usageProviderTotal,
	}
	if err := harness.ValidateUsage(usage); err != nil {
		return harness.Usage{}, nil, err
	}
	if reasoning > output {
		return harness.Usage{}, nil, errors.New("reasoning tokens exceed output tokens")
	}
	return usage, providerTotalTokens, nil
}

func nonnegativeInteger(object map[string]any, key string) (int64, error) {
	value, present, err := optionalNonnegativeInteger(object, key)
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, fmt.Errorf("json field %q is required", key)
	}
	return value, nil
}

func optionalNonnegativeInteger(object map[string]any, key string) (int64, bool, error) {
	value, ok := object[key]
	if !ok {
		return 0, false, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("json field %q must be an integer", key)
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || parsed < 0 {
		return 0, false, fmt.Errorf("json field %q must be a nonnegative integer", key)
	}
	return parsed, true, nil
}

func completedSSEResponse(
	body []byte,
	maxEventBytes int,
	maxEvents int,
) (
	map[string]any,
	[]harnesscodex.ResponsesSSEEventTrace,
	*int,
	error,
) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes+1)
	var data strings.Builder
	var eventName string
	var completed map[string]any
	var completedSequence *int
	traces := make([]harnesscodex.ResponsesSSEEventTrace, 0)
	events := 0
	done := false
	dispatch := func() error {
		if data.Len() == 0 {
			eventName = ""
			return nil
		}
		events++
		if events > maxEvents {
			return errors.New("upstream SSE exceeds its event limit")
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if len(payload) > maxEventBytes {
			return errors.New("upstream SSE event exceeds its byte limit")
		}
		if payload == "[DONE]" {
			if eventName != "" {
				return errors.New("upstream SSE [DONE] unexpectedly has an event field")
			}
			traces = append(traces, harnesscodex.ResponsesSSEEventTrace{
				Sequence:   len(traces),
				Type:       "[DONE]",
				DataSHA256: bytesDigest([]byte(payload)),
				Mapping:    harnesscodex.SSEMappingStreamDone,
			})
			done = true
			eventName = ""
			return nil
		}
		if done {
			return errors.New("upstream SSE data appeared after [DONE]")
		}
		event, err := decodeJSONObject([]byte(payload))
		if err != nil {
			return fmt.Errorf("decode upstream SSE event: %w", err)
		}
		typeName, err := requiredString(event, "type")
		if err != nil {
			return err
		}
		if len(typeName) > 256 || len(eventName) > 256 {
			return errors.New("upstream SSE event identity exceeds its byte limit")
		}
		if eventName != "" && eventName != typeName {
			return errors.New("upstream SSE event field differs from JSON event type")
		}
		canonicalEventSHA256, err := canonicalJSONDigest(event)
		if err != nil {
			return err
		}
		trace := harnesscodex.ResponsesSSEEventTrace{
			Sequence:            len(traces),
			EventPresent:        eventName != "",
			Event:               eventName,
			Type:                typeName,
			DataSHA256:          bytesDigest([]byte(payload)),
			CanonicalJSONSHA256: canonicalEventSHA256,
			Mapping:             harnesscodex.SSEMappingForwardedUnmapped,
		}
		switch typeName {
		case "response.completed":
			if completed != nil {
				return errors.New("upstream SSE contains multiple completed responses")
			}
			completed, err = requiredObject(event, "response")
			if err != nil {
				return err
			}
			mappedSHA256, err := canonicalJSONDigest(completed)
			if err != nil {
				return err
			}
			trace.Mapping = harnesscodex.SSEMappingCompletedResponse
			trace.MappedResponseSHA256 = mappedSHA256
			sequence := trace.Sequence
			completedSequence = &sequence
		case "response.failed", "response.incomplete", "error":
			return fmt.Errorf("upstream SSE ended with %s", typeName)
		}
		traces = append(traces, trace)
		eventName = ""
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			if eventName != "" {
				return nil, nil, nil, errors.New("upstream SSE event has duplicate event fields")
			}
			eventName = value
		case "data":
			if data.Len()+len(value)+1 > maxEventBytes {
				return nil, nil, nil, errors.New("upstream SSE event exceeds its byte limit")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		default:
			return nil, nil, nil, fmt.Errorf("unsupported upstream SSE field %q", field)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("scan upstream SSE: %w", err)
	}
	if err := dispatch(); err != nil {
		return nil, nil, nil, err
	}
	if completed == nil {
		return nil, nil, nil, errors.New("upstream SSE omitted response.completed")
	}
	return completed, traces, completedSequence, nil
}

func comparePairSnapshots(baseline, candidate armSnapshot) error {
	if baseline.ordinary != candidate.ordinary {
		return errors.New("paired Codex terminal states are asymmetric")
	}
	if baseline.requestCount != candidate.requestCount {
		return errors.New("paired Codex provider request counts drifted")
	}
	if baseline.trace.TLSRequired != candidate.trace.TLSRequired {
		return errors.New("paired Codex production TLS modes drifted")
	}
	if baseline.captureError != "" || candidate.captureError != "" {
		return errors.New("paired Codex capture contains an integrity failure")
	}
	if baseline.requestCount == 0 {
		if baseline.trace.FirstRequest != nil || candidate.trace.FirstRequest != nil {
			return errors.New("paired Codex empty request snapshots are inconsistent")
		}
		return compareEffectiveConfigs(baseline, candidate)
	}
	if baseline.trace.FirstRequest == nil || candidate.trace.FirstRequest == nil {
		return errors.New("paired Codex trace omitted first request")
	}
	baselineFirst := baseline.trace.FirstRequest
	candidateFirst := candidate.trace.FirstRequest
	if baselineFirst.Model != candidateFirst.Model {
		return errors.New("paired Codex request model drifted")
	}
	if baselineFirst.NonceNormalizedNonToolPayloadSHA256 != candidateFirst.NonceNormalizedNonToolPayloadSHA256 {
		return errors.New("paired Codex nonce-normalized request parity projection drifted")
	}
	if len(baseline.trace.Requests) != baseline.requestCount ||
		len(candidate.trace.Requests) != candidate.requestCount {
		return errors.New("paired Codex ordered request snapshots are incomplete")
	}
	for index := range baseline.requestCount {
		left := baseline.trace.Requests[index]
		right := candidate.trace.Requests[index]
		if left.Sequence != right.Sequence || left.Model != right.Model ||
			left.NonceNormalizedNonToolPayloadSHA256 != right.NonceNormalizedNonToolPayloadSHA256 ||
			left.Headers.ParityProjectionSHA256 != right.Headers.ParityProjectionSHA256 {
			return fmt.Errorf("paired Codex request %d reviewed parity projection drifted", index)
		}
		if !sameDynamicFieldClassifications(left.DynamicFields, right.DynamicFields) {
			return fmt.Errorf("paired Codex request %d dynamic-field classifications drifted", index)
		}
	}
	baselineTLS, err := tlsIdentitySet(baseline.trace)
	if err != nil {
		return err
	}
	candidateTLS, err := tlsIdentitySet(candidate.trace)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(baselineTLS, candidateTLS) {
		return errors.New("paired Codex production TLS identities drifted")
	}
	baselineCommands, baselineMCP, baselineSupport := splitDeclarations(baselineFirst.Tools)
	candidateCommands, candidateMCP, candidateSupport := splitDeclarations(candidateFirst.Tools)
	if len(baselineMCP) != 0 || len(baselineSupport) != 0 {
		return errors.New("baseline Codex trace contains MCP declarations")
	}
	if !reflect.DeepEqual(baselineCommands, candidateCommands) {
		return errors.New("paired Codex command tool surface drifted")
	}
	wanted := harnesscodex.AllowedMCPTools()
	if len(candidateMCP) != len(wanted) {
		return errors.New("candidate Codex trace does not contain exactly four repo_view declarations")
	}
	seen := make(map[string]struct{}, len(candidateMCP))
	for _, declaration := range candidateMCP {
		seen[declaration.Name] = struct{}{}
	}
	for _, name := range wanted {
		if _, ok := seen["repo_view."+name]; !ok {
			return fmt.Errorf("candidate Codex trace omitted repo_view.%s", name)
		}
	}
	supportWanted := harnesscodex.AllowedMCPSupportTools()
	if len(candidateSupport) != len(supportWanted) {
		return errors.New("candidate Codex trace does not contain exactly three MCP support declarations")
	}
	supportSeen := make(map[string]struct{}, len(candidateSupport))
	for _, declaration := range candidateSupport {
		supportSeen[declaration.Name] = struct{}{}
	}
	for _, name := range supportWanted {
		if _, ok := supportSeen[name]; !ok {
			return fmt.Errorf("candidate Codex trace omitted MCP support tool %s", name)
		}
	}
	return compareEffectiveConfigs(baseline, candidate)
}

func sameDynamicFieldClassifications(
	left, right []harnesscodex.ProviderRequestDynamicFieldTrace,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].JSONPointer != right[index].JSONPointer ||
			left[index].Present != right[index].Present ||
			left[index].Classification != right[index].Classification {
			return false
		}
	}
	return true
}

func tlsIdentitySet(trace harnesscodex.ResponsesTrace) ([]string, error) {
	identities := make(map[string]struct{})
	commit := func(connections []harnesscodex.TLSConnectionTrace) error {
		for _, connection := range connections {
			raw, err := json.Marshal(connection)
			if err != nil {
				return errors.New("encode production TLS connection identity")
			}
			identities[bytesDigest(raw)] = struct{}{}
		}
		return nil
	}
	for _, attempt := range trace.ResponseAttempts {
		if err := commit(attempt.TLSConnections); err != nil {
			return nil, err
		}
	}
	for _, response := range trace.Responses {
		if err := commit(response.TLSConnections); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result, nil
}

func splitDeclarations(
	declarations []harnesscodex.ResponsesToolDeclaration,
) (
	[]harnesscodex.ResponsesToolDeclaration,
	[]harnesscodex.ResponsesToolDeclaration,
	[]harnesscodex.ResponsesToolDeclaration,
) {
	commands := make([]harnesscodex.ResponsesToolDeclaration, 0, len(declarations))
	mcp := make([]harnesscodex.ResponsesToolDeclaration, 0, 4)
	support := make([]harnesscodex.ResponsesToolDeclaration, 0, 3)
	for _, declaration := range declarations {
		switch declaration.Kind {
		case harnesscodex.ToolKindMCP:
			mcp = append(mcp, declaration)
		case harnesscodex.ToolKindMCPSupport:
			support = append(support, declaration)
		default:
			commands = append(commands, declaration)
		}
	}
	return commands, mcp, support
}
