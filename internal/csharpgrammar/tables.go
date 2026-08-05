package csharpgrammar

import (
	_ "embed"
	"encoding/binary"
)

const (
	csharpGeneratedTableMagic       = "RVCSHARP"
	csharpGeneratedTableHeaderBytes = 16
)

//go:embed language_tables.bin
var csharpGeneratedTableData []byte

func csharpGeneratedParseTables() ([]uint16, []uint16) {
	data := csharpGeneratedTableData
	if len(data) < csharpGeneratedTableHeaderBytes ||
		string(data[:8]) != csharpGeneratedTableMagic {
		panic("invalid generated C# parse-table header")
	}
	parseCount := int(binary.LittleEndian.Uint32(data[8:12]))
	smallCount := int(binary.LittleEndian.Uint32(data[12:16]))
	if parseCount < 0 || smallCount < 0 ||
		parseCount > (len(data)-csharpGeneratedTableHeaderBytes)/2 ||
		smallCount > (len(data)-csharpGeneratedTableHeaderBytes)/2-parseCount ||
		len(data) != csharpGeneratedTableHeaderBytes+2*(parseCount+smallCount) {
		panic("invalid generated C# parse-table length")
	}

	parseTable := make([]uint16, parseCount)
	smallParseTable := make([]uint16, smallCount)
	offset := csharpGeneratedTableHeaderBytes
	for _, table := range [][]uint16{parseTable, smallParseTable} {
		for index := range table {
			table[index] = binary.LittleEndian.Uint16(data[offset : offset+2])
			offset += 2
		}
	}
	return parseTable, smallParseTable
}
