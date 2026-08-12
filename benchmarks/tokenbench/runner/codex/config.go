package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	harnesscodex "github.com/yapless/scopesifter/benchmarks/tokenbench/harness/codex"
	genericrunner "github.com/yapless/scopesifter/benchmarks/tokenbench/runner"
)

func (lifecycle *Lifecycle) readEffectiveConfig(ctx context.Context) (_ []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directoryBefore, err := lifecycle.stateRoot.Lstat("config-lock")
	if err != nil || directoryBefore.Mode()&os.ModeSymlink != 0 ||
		!directoryBefore.IsDir() || directoryBefore.Mode().Perm() != 0o700 {
		return nil, errors.New("codex config-lock directory is not the private runtime directory")
	}
	directory, err := lifecycle.stateRoot.OpenRoot("config-lock")
	if err != nil {
		return nil, fmt.Errorf("open Codex config-lock directory: %w", err)
	}
	defer func() {
		resultErr = joinCloseError(resultErr, directory, "close Codex config-lock directory")
	}()
	directoryOpened, err := directory.Stat(".")
	if err != nil || !os.SameFile(directoryBefore, directoryOpened) {
		return nil, errors.New("codex config-lock directory changed while opening")
	}
	directoryFile, err := directory.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open Codex config-lock directory for listing: %w", err)
	}
	entries, readErr := directoryFile.ReadDir(-1)
	closeErr := directoryFile.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("codex config-lock directory contains %d entries, want exactly one", len(entries))
	}
	name := entries[0].Name()
	before, err := directory.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("lstat Codex effective config: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Mode().Perm()&0o022 != 0 || hasMultipleLinks(before) {
		return nil, errors.New("codex effective config is not a private, single-link regular file")
	}
	if before.Size() <= 0 || before.Size() > harnesscodex.MaxEffectiveConfigBytes {
		return nil, errors.New("codex effective config is empty or exceeds its byte limit")
	}
	file, err := directory.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open Codex effective config: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, joinCloseError(
			errors.New("codex effective config changed while opening"),
			file,
			"close Codex effective config",
		)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, harnesscodex.MaxEffectiveConfigBytes+1))
	closeErr = file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(content) == 0 || len(content) > harnesscodex.MaxEffectiveConfigBytes {
		return nil, errors.New("codex effective config is empty or exceeds its byte limit")
	}
	after, err := directory.Lstat(name)
	if err != nil || !os.SameFile(opened, after) || before.Mode() != after.Mode() ||
		before.Size() != after.Size() || before.ModTime() != after.ModTime() ||
		hasMultipleLinks(after) {
		return nil, errors.New("codex effective config changed while reading")
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("codex effective config contains invalid text")
	}
	if bytes.Contains(content, []byte(lifecycle.credential)) {
		return nil, errors.New("codex effective config contains the real upstream credential")
	}
	if bytes.Contains(content, []byte("CODEX_API_KEY")) ||
		bytes.Contains(content, []byte(lifecycle.layout.LocalProxyCapability)) {
		return nil, errors.New("codex effective config contains credential-shaped launch data")
	}
	directoryAfter, err := lifecycle.stateRoot.Lstat("config-lock")
	if err != nil || !os.SameFile(directoryOpened, directoryAfter) ||
		directoryAfter.Mode() != directoryBefore.Mode() {
		return nil, errors.New("codex config-lock directory changed while reading")
	}
	return content, nil
}

func compareEffectiveConfigs(baseline, candidate armSnapshot) error {
	if len(baseline.configDelta) != 0 {
		return errors.New("baseline effective config contains scopesifter configuration")
	}
	if len(candidate.configDelta) == 0 {
		return errors.New("candidate effective config omitted scopesifter configuration")
	}
	if !bytes.Equal(baseline.configCommon, candidate.configCommon) {
		return errors.New("paired Codex effective configuration drifted outside scopesifter")
	}
	return nil
}

type effectiveConfigLock struct {
	Config       map[string]any `json:"Config"       toml:"config"`
	CodexVersion string         `json:"CodexVersion" toml:"codex_version"`
	Version      int64          `json:"Version"      toml:"version"`
}

func normalizeEffectiveConfig(
	raw []byte,
	arm genericrunner.Arm,
	registration *harness.MCPServer,
) ([]byte, []byte, error) {
	if len(raw) == 0 || len(raw) > harnesscodex.MaxEffectiveConfigBytes ||
		!utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, nil, errors.New("effective config is empty, oversized, or invalid text")
	}
	if bytes.IndexByte(raw, '\r') >= 0 {
		return nil, nil, errors.New("effective config must use canonical LF line endings")
	}
	var lock effectiveConfigLock
	decoder := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return nil, nil, fmt.Errorf("decode Codex v0.144.0 config lock: %w", err)
	}
	if lock.Version != 1 || lock.CodexVersion != "0.144.0" || lock.Config == nil {
		return nil, nil, errors.New("effective config has an unsupported lock envelope")
	}
	serversValue, exists := lock.Config["mcp_servers"]
	if !exists {
		return nil, nil, errors.New("effective config omitted the resolved MCP registry")
	}
	servers, ok := serversValue.(map[string]any)
	if !ok || servers == nil {
		return nil, nil, errors.New("effective config MCP registry is not a table")
	}
	scopeSifterValue, hasScopeSifter := servers["scopesifter"]
	delete(servers, "scopesifter")
	if len(servers) != 0 {
		return nil, nil, errors.New("effective config contains an MCP server other than scopesifter")
	}

	var delta []byte
	switch arm {
	case genericrunner.BaselineArm:
		if hasScopeSifter || registration != nil {
			return nil, nil, errors.New("baseline effective config contains scopesifter")
		}
	case genericrunner.CandidateArm:
		if !hasScopeSifter || registration == nil {
			return nil, nil, errors.New("candidate effective config omitted scopesifter")
		}
		scopeSifter, ok := scopeSifterValue.(map[string]any)
		if !ok || scopeSifter == nil {
			return nil, nil, errors.New("candidate scopesifter effective config is not a table")
		}
		if err := validateEffectiveScopeSifter(scopeSifter, *registration); err != nil {
			return nil, nil, err
		}
		var err error
		delta, err = json.Marshal(scopeSifter)
		if err != nil {
			return nil, nil, fmt.Errorf("canonicalize scopesifter effective config: %w", err)
		}
	default:
		return nil, nil, fmt.Errorf("unsupported arm %q", arm)
	}
	common, err := json.Marshal(lock)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize common effective config: %w", err)
	}
	return common, delta, nil
}

func validateEffectiveScopeSifter(
	observed map[string]any,
	registration harness.MCPServer,
) error {
	wanted := map[string]any{
		"command":                     registration.Command,
		"args":                        append([]string(nil), registration.Arguments...),
		"env":                         map[string]any{},
		"environment_id":              "local",
		"enabled":                     true,
		"required":                    true,
		"startup_timeout_sec":         float64(10),
		"tool_timeout_sec":            float64(60),
		"default_tools_approval_mode": "auto",
		"enabled_tools":               harnesscodex.AllowedMCPTools(),
		"disabled_tools":              []string{},
	}
	canonicalObserved, err := normalizeTOMLValue(observed)
	if err != nil {
		return fmt.Errorf("normalize scopesifter effective config: %w", err)
	}
	canonicalWanted, err := normalizeTOMLValue(wanted)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(canonicalObserved, canonicalWanted) {
		return errors.New("candidate effective scopesifter config differs from the approved registration")
	}
	return nil
}

func normalizeTOMLValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizeTOMLValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			normalized, err := normalizeTOMLValue(child)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case []string:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = child
		}
		return result, nil
	case int64:
		return float64(value), nil
	case float64, string, bool:
		return value, nil
	case nil:
		return nil, errors.New("effective config contains an unsupported null value")
	default:
		return nil, fmt.Errorf("effective config contains unsupported TOML value type %T", value)
	}
}
