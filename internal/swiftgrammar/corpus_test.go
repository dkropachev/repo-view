package swiftgrammar

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

var (
	swiftCorpusHeaderRE     = regexp.MustCompile(`(?m)^(={3,})\r?\n([^\r\n]+)\r?\n={3,}\r?\n`)
	swiftCorpusDividerRE    = regexp.MustCompile(`(?m)^-{3,}\r?\n`)
	swiftCorpusWhitespaceRE = regexp.MustCompile(`\s+`)
	swiftCorpusFieldRE      = regexp.MustCompile(` \w+: \(`)
)

type swiftCorpusCase struct {
	name, source, expected string
}

func TestSwiftPinnedUpstreamCorpus(t *testing.T) {
	const root = "testdata/tree-sitter-swift-corpus"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read pinned Swift corpus: %v", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".txt" {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	if len(paths) != 10 {
		t.Fatalf("pinned Swift corpus files = %d, want 10", len(paths))
	}
	sort.Strings(paths)
	parser := treesitterparser.NewParser()
	parser.SetLanguage(Language())
	count := 0
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, test := range swiftParseCorpus(string(data)) {
			count++
			t.Run(filepath.Base(path)+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				tree := parser.ParseString(ctx, []byte(test.source))
				if tree == nil || ctx.Err() != nil {
					t.Fatalf("parse failed: tree=%v err=%v", tree, ctx.Err())
				}
				got := swiftNormalizeCorpus(tree.RootNode().String())
				if !swiftCorpusFieldRE.MatchString(test.expected) {
					got = swiftCorpusFieldRE.ReplaceAllString(got, " (")
				}
				if got != test.expected {
					t.Fatalf("tree mismatch\nsource:\n%s\ngot:\n%s\nwant:\n%s", test.source, got, test.expected)
				}
			})
		}
	}
	if count != 242 {
		t.Fatalf("corpus cases = %d, want 242", count)
	}
}

func swiftParseCorpus(data string) []swiftCorpusCase {
	headers := swiftCorpusHeaderRE.FindAllStringSubmatchIndex(data, -1)
	result := make([]swiftCorpusCase, 0, len(headers))
	for index, header := range headers {
		regionEnd := len(data)
		if index+1 < len(headers) {
			regionEnd = headers[index+1][0]
		}
		region := data[header[1]:regionEnd]
		dividers := swiftCorpusDividerRE.FindAllStringIndex(region, -1)
		if len(dividers) == 0 {
			continue
		}
		divider := dividers[0]
		source := strings.TrimRight(region[:divider[0]], "\r\n")
		source = strings.TrimPrefix(source, "\r\n")
		source = strings.TrimPrefix(source, "\n")
		if source != "" {
			source += "\n"
		}
		expected := swiftNormalizeCorpus(region[divider[1]:])
		result = append(result, swiftCorpusCase{
			name: data[header[4]:header[5]], source: source, expected: expected,
		})
	}
	return result
}

func swiftNormalizeCorpus(value string) string {
	value = strings.TrimSpace(value)
	value = swiftCorpusWhitespaceRE.ReplaceAllString(value, " ")
	return strings.ReplaceAll(value, " )", ")")
}
