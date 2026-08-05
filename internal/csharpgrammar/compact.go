//go:build ignore

// Command compact moves the two largest generated uint16 table literals into
// a deterministic binary asset. It is invoked by generate.sh, not built with
// the grammar package.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	parseTablePrefix      = "\tparseTable := []uint16{\n"
	smallParseTablePrefix = "\tsmallParseTable := []uint16{\n"
	tableSuffix           = "\t}\n\n"
	tableMagic            = "RVCSHARP"
	tableHeaderBytes      = 16
)

type literalBlock struct {
	values     []uint16
	start, end int
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run compact.go GENERATED_GO OUTPUT_BIN")
		os.Exit(2)
	}
	sourcePath, tablePath := os.Args[1], os.Args[2]
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		fatalf("read generated source: %v", err)
	}
	parseTable, err := findLiteralBlock(source, parseTablePrefix)
	if err != nil {
		fatalf("parse dense table: %v", err)
	}
	smallParseTable, err := findLiteralBlock(source, smallParseTablePrefix)
	if err != nil {
		fatalf("parse compressed table: %v", err)
	}
	if parseTable.end > smallParseTable.start {
		fatalf("generated table declarations overlap")
	}

	replacement := []byte("\tparseTable, smallParseTable := csharpGeneratedParseTables()\n\n")
	rewritten := make(
		[]byte,
		0,
		len(source)-(parseTable.end-parseTable.start)-
			(smallParseTable.end-smallParseTable.start)+len(replacement),
	)
	rewritten = append(rewritten, source[:parseTable.start]...)
	rewritten = append(rewritten, replacement...)
	rewritten = append(rewritten, source[parseTable.end:smallParseTable.start]...)
	rewritten = append(rewritten, source[smallParseTable.end:]...)
	if err := os.WriteFile(sourcePath, rewritten, 0o644); err != nil {
		fatalf("write compact generated source: %v", err)
	}

	tableBytes := encodeTables(parseTable.values, smallParseTable.values)
	if err := os.WriteFile(tablePath, tableBytes, 0o644); err != nil {
		fatalf("write generated table asset: %v", err)
	}
}

func findLiteralBlock(source []byte, prefix string) (literalBlock, error) {
	start := bytes.Index(source, []byte(prefix))
	if start < 0 {
		return literalBlock{}, fmt.Errorf("declaration %q not found", strings.TrimSpace(prefix))
	}
	bodyStart := start + len(prefix)
	relativeEnd := bytes.Index(source[bodyStart:], []byte(tableSuffix))
	if relativeEnd < 0 {
		return literalBlock{}, fmt.Errorf("declaration %q has no end", strings.TrimSpace(prefix))
	}
	bodyEnd := bodyStart + relativeEnd
	var uncommented strings.Builder
	for line := range strings.Lines(string(source[bodyStart:bodyEnd])) {
		if comment := strings.Index(line, "//"); comment >= 0 {
			line = line[:comment]
		}
		uncommented.WriteString(line)
	}
	fields := strings.FieldsFunc(uncommented.String(), func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t' ||
			character == '\r' || character == '\n'
	})
	values := make([]uint16, 0, len(fields))
	for index, field := range fields {
		value, err := strconv.ParseUint(field, 10, 16)
		if err != nil {
			return literalBlock{}, fmt.Errorf("value %d %q: %w", index, field, err)
		}
		values = append(values, uint16(value))
	}
	return literalBlock{
		start:  start,
		end:    bodyEnd + len(tableSuffix),
		values: values,
	}, nil
}

func encodeTables(parseTable, smallParseTable []uint16) []byte {
	result := make(
		[]byte,
		tableHeaderBytes+2*(len(parseTable)+len(smallParseTable)),
	)
	copy(result, tableMagic)
	binary.LittleEndian.PutUint32(result[8:12], uint32(len(parseTable)))
	binary.LittleEndian.PutUint32(result[12:16], uint32(len(smallParseTable)))
	offset := tableHeaderBytes
	for _, table := range [][]uint16{parseTable, smallParseTable} {
		for _, value := range table {
			binary.LittleEndian.PutUint16(result[offset:offset+2], value)
			offset += 2
		}
	}
	return result
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
