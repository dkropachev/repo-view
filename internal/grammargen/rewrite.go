package grammargen

import (
	"bytes"
	"fmt"
)

var (
	internalCoreImport = []byte(`core "github.com/dcosson/treesitter-go/internal/core"`)
	publicCoreImport   = []byte(`core "github.com/dcosson/treesitter-go"`)
	languageImport     = []byte(`language "github.com/dcosson/treesitter-go/language"`)
	internalLanguage   = []byte("language.Language")
	publicLanguage     = []byte("core.Language")
	legacyABIVersion   = []byte("Version:                14,")
	pinnedABIVersion   = []byte("Version:                15,")
)

func rewriteGenerated(source []byte, correctABIVersion bool) ([]byte, error) {
	if bytes.Count(source, internalCoreImport) != 1 {
		return nil, fmt.Errorf("generated source must contain exactly one internal core import")
	}
	if bytes.Count(source, languageImport) != 1 {
		return nil, fmt.Errorf("generated source must contain exactly one internal language import")
	}
	if !bytes.Contains(source, internalLanguage) {
		return nil, fmt.Errorf("generated source does not reference language.Language")
	}

	rewritten := bytes.Replace(source, internalCoreImport, publicCoreImport, 1)
	rewritten = removeLineContaining(rewritten, languageImport)
	rewritten = bytes.ReplaceAll(rewritten, internalLanguage, publicLanguage)
	if correctABIVersion {
		if bytes.Count(rewritten, legacyABIVersion) != 1 {
			return nil, fmt.Errorf("generated C# source must contain exactly one ABI 14 metadata field")
		}
		rewritten = bytes.Replace(rewritten, legacyABIVersion, pinnedABIVersion, 1)
	}
	return rewritten, nil
}

func removeLineContaining(source, needle []byte) []byte {
	result := make([]byte, 0, len(source))
	for len(source) > 0 {
		lineEnd := bytes.IndexByte(source, '\n')
		if lineEnd < 0 {
			if !bytes.Contains(source, needle) {
				result = append(result, source...)
			}
			break
		}
		lineEnd++
		line := source[:lineEnd]
		if !bytes.Contains(line, needle) {
			result = append(result, line...)
		}
		source = source[lineEnd:]
	}
	return result
}
