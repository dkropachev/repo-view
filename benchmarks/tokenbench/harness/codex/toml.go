package codex

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// tomlString emits one TOML v1.0 basic string. It never emits a literal line
// break, so every -c assignment remains exactly one argv element.
func tomlString(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("toml string must be valid UTF-8")
	}
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("toml string must not contain NUL")
	}
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
	return result.String(), nil
}

func tomlStringArray(values []string) (string, error) {
	if values == nil {
		return "", errors.New("toml array must be canonical, not nil")
	}
	encoded := make([]string, len(values))
	for index, value := range values {
		item, err := tomlString(value)
		if err != nil {
			return "", fmt.Errorf("toml array element %d: %w", index, err)
		}
		encoded[index] = item
	}
	return "[" + strings.Join(encoded, ",") + "]", nil
}

func tomlStringMap(values map[string]string) (string, error) {
	if values == nil {
		return "", errors.New("toml map must be canonical, not nil")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !validBareTOMLKey(key) {
			return "", fmt.Errorf("invalid bare TOML key %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		value, err := tomlString(values[key])
		if err != nil {
			return "", fmt.Errorf("toml map value for %q: %w", key, err)
		}
		encoded = append(encoded, key+"="+value)
	}
	return "{" + strings.Join(encoded, ",") + "}", nil
}

func validBareTOMLKey(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
