package taskctl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"unicode"
	"unicode/utf8"
)

const (
	maximumTaskctlInputBytes     = 256 << 20
	maximumTaskctlJSONDepth      = 128
	maximumTaskctlJSONObjectKeys = 1_000_000
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validateUniqueJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	keyCount := 0
	if err := validateUniqueJSONValue(decoder, 0, &keyCount); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func validateUniqueJSONValue(decoder *json.Decoder, depth int, keyCount *int) error {
	if depth > maximumTaskctlJSONDepth {
		return fmt.Errorf("JSON nesting exceeds depth %d", maximumTaskctlJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]string)
		for decoder.More() {
			*keyCount++
			if *keyCount > maximumTaskctlJSONObjectKeys {
				return fmt.Errorf("JSON exceeds %d object keys", maximumTaskctlJSONObjectKeys)
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			foldedKey := foldJSONKey(key)
			if previous, duplicate := keys[foldedKey]; duplicate {
				if previous == key {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				return fmt.Errorf("case-fold duplicate JSON object keys %q and %q", previous, key)
			}
			keys[foldedKey] = key
			if err := validateUniqueJSONValue(decoder, depth+1, keyCount); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object has an invalid closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder, depth+1, keyCount); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array has an invalid closing delimiter")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func foldJSONKey(key string) string {
	folded := make([]byte, 0, len(key))
	for index := 0; index < len(key); {
		value, width := utf8.DecodeRuneInString(key[index:])
		index += width
		if 'a' <= value && value <= 'z' {
			value -= 'a' - 'A'
		} else if value >= utf8.RuneSelf {
			for {
				next := unicode.SimpleFold(value)
				if next <= value {
					break
				}
				value = next
			}
		}
		folded = utf8.AppendRune(folded, value)
	}
	return string(folded)
}

func decodeSourceAuditJSON(data []byte, destination any) error {
	if err := validateUniqueJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make regular input absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absolute) != filepath.Clean(resolved) {
		return nil, errors.New("regular input path must not traverse symlinks")
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || !sourceAuditFileHasOneLink(before) {
		return nil, errors.New("not a single-link regular non-symlink file")
	}
	if before.Size() < 0 || before.Size() > maximumTaskctlInputBytes {
		return nil, fmt.Errorf("regular input exceeds %d bytes", maximumTaskctlInputBytes)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(statErr, file.Close())
	}
	if !opened.Mode().IsRegular() || !sourceAuditFileHasOneLink(opened) ||
		!os.SameFile(before, opened) || before.Mode() != opened.Mode() ||
		before.Size() != opened.Size() {
		return nil, errors.Join(errors.New("file changed while opening"), file.Close())
	}
	reader := bufio.NewReader(io.LimitReader(file, maximumTaskctlInputBytes+1))
	data, readErr := io.ReadAll(reader)
	after, afterErr := file.Stat()
	pathAfter, pathErr := os.Lstat(absolute)
	closeErr := file.Close()
	if err := errors.Join(readErr, afterErr, pathErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > maximumTaskctlInputBytes {
		return nil, fmt.Errorf("regular input exceeds %d bytes", maximumTaskctlInputBytes)
	}
	if !after.Mode().IsRegular() || !pathAfter.Mode().IsRegular() ||
		!sourceAuditFileHasOneLink(after) || !sourceAuditFileHasOneLink(pathAfter) ||
		!os.SameFile(opened, after) || !os.SameFile(after, pathAfter) ||
		opened.Mode() != after.Mode() || after.Mode() != pathAfter.Mode() ||
		opened.Size() != after.Size() || after.Size() != pathAfter.Size() ||
		opened.ModTime() != after.ModTime() || after.ModTime() != pathAfter.ModTime() ||
		int64(len(data)) != after.Size() {
		return nil, errors.New("file changed while reading")
	}
	return data, nil
}
