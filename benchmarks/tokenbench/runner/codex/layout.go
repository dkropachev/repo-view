package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
)

const (
	// OfflineLocalProxyCapability is inert and only valid in offline audit
	// plans. A live Lifecycle supplies a fresh capability instead; publication
	// is unavailable until that lifecycle is closed and the committed value is
	// therefore expired. Neither value is an upstream credential.
	OfflineLocalProxyCapability = harness.OfflineLocalProxyCapability

	responsesPath = "/v1/responses"
	layoutSchema  = "tokenbench.codex-runtime-layout/v4"
)

// RuntimeLayout is the code-owned, publishable launch layout shared by the
// Codex adapter and lifecycle. ToolboxRoot is the sole PATH directory and is
// disjoint from all writable runtime directories. All values are identical for
// both arms.
type RuntimeLayout struct {
	ProxyURL             string `json:"proxy_url"`
	Home                 string `json:"home"`
	CodexHome            string `json:"codex_home"`
	Temp                 string `json:"temp"`
	ConfigLock           string `json:"config_lock"`
	ToolboxRoot          string `json:"toolbox_root"`
	LocalProxyCapability string `json:"local_proxy_capability"`
}

// Validate checks the exact v0.144.0 runtime layout contract.
func (layout RuntimeLayout) Validate() error {
	if !harness.ValidLocalProxyCapability(layout.LocalProxyCapability) {
		return errors.New("codex runtime local proxy capability is not canonical")
	}
	proxy, err := url.Parse(layout.ProxyURL)
	if err != nil {
		return fmt.Errorf("parse Codex proxy URL: %w", err)
	}
	if proxy.Scheme != "http" || proxy.Hostname() != "127.0.0.1" ||
		proxy.Port() == "" || proxy.Path != "/v1" || proxy.RawPath != "" ||
		proxy.RawQuery != "" || proxy.Fragment != "" || proxy.User != nil {
		return errors.New("codex proxy URL must be canonical http://127.0.0.1:PORT/v1")
	}
	if canonical := "http://" + net.JoinHostPort(proxy.Hostname(), proxy.Port()) + "/v1"; layout.ProxyURL != canonical {
		return fmt.Errorf("codex proxy URL is not canonical: got %q, want %q", layout.ProxyURL, canonical)
	}

	paths := []struct {
		name  string
		value string
	}{
		{"HOME", layout.Home},
		{"CODEX_HOME", layout.CodexHome},
		{"TMPDIR", layout.Temp},
		{"config lock", layout.ConfigLock},
		{"PATH toolbox", layout.ToolboxRoot},
	}
	for _, path := range paths {
		if !validText(path.value) || !filepath.IsAbs(path.value) ||
			filepath.Clean(path.value) != path.value || isFilesystemRoot(path.value) {
			return fmt.Errorf("codex runtime %s must be an absolute, clean, non-root path", path.name)
		}
	}
	if strings.ContainsRune(layout.ToolboxRoot, os.PathListSeparator) {
		return errors.New("codex runtime PATH toolbox must be exactly one directory")
	}
	for first := range paths {
		for second := first + 1; second < len(paths); second++ {
			if pathsOverlap(paths[first].value, paths[second].value) {
				return fmt.Errorf(
					"codex runtime paths %s and %s overlap",
					paths[first].name,
					paths[second].name,
				)
			}
		}
	}
	return nil
}

// Environment returns the exact complete environment the adapter must commit
// into both arm ProcessSpecs.
func (layout RuntimeLayout) Environment() map[string]string {
	return map[string]string{
		"CODEX_API_KEY":     layout.LocalProxyCapability,
		"CODEX_HOME":        layout.CodexHome,
		"CODEX_SQLITE_HOME": filepath.Join(layout.CodexHome, "sqlite"),
		"HOME":              layout.Home,
		"PATH":              layout.ToolboxRoot,
		"TMPDIR":            layout.Temp,
	}
}

// ConfigAssignments returns the exact common Codex -c assignments owned by
// the runtime layout. Callers interleave each value with a preceding "-c".
func (layout RuntimeLayout) ConfigAssignments() []string {
	return []string{
		"openai_base_url=" + tomlString(layout.ProxyURL),
		"debug.config_lockfile.export_dir=" + tomlString(layout.ConfigLock),
		"debug.config_lockfile.save_fields_resolved_from_model_catalog=true",
		"shell_environment_policy.set={PATH=" + tomlString(layout.ToolboxRoot) + "}",
	}
}

// Commitment returns a deterministic SHA-256 commitment to the publishable
// layout. It never contains or depends on the real upstream credential.
func (layout RuntimeLayout) Commitment() (string, error) {
	if err := layout.Validate(); err != nil {
		return "", err
	}
	canonical := struct {
		Schema string        `json:"schema"`
		Layout RuntimeLayout `json:"layout"`
	}{Schema: layoutSchema, Layout: layout}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode Codex runtime layout: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func tomlString(value string) string {
	var result strings.Builder
	result.Grow(len(value) + 2)
	result.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			result.WriteString(`\"`)
		case '\\':
			result.WriteString(`\\`)
		case '\b':
			result.WriteString(`\b`)
		case '\t':
			result.WriteString(`\t`)
		case '\n':
			result.WriteString(`\n`)
		case '\f':
			result.WriteString(`\f`)
		case '\r':
			result.WriteString(`\r`)
		default:
			if character < 0x20 || character == 0x7f {
				fmt.Fprintf(&result, `\u%04X`, character)
				continue
			}
			result.WriteRune(character)
		}
	}
	result.WriteByte('"')
	return result.String()
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func isFilesystemRoot(path string) bool {
	volume := filepath.VolumeName(path)
	return filepath.Clean(path) == filepath.Join(volume, string(filepath.Separator))
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
