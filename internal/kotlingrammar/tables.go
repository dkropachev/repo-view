package kotlingrammar

import (
	_ "embed"
	"encoding/binary"
)

const (
	kotlinGeneratedTableMagic       = "RVKOTLIN"
	kotlinGeneratedTableHeaderBytes = 16
)

//go:embed language_tables.bin
var kotlinGeneratedTableData []byte

func kotlinGeneratedParseTables() ([]uint16, []uint16) {
	data := kotlinGeneratedTableData
	if len(data) < kotlinGeneratedTableHeaderBytes ||
		string(data[:8]) != kotlinGeneratedTableMagic {
		panic("invalid generated Kotlin parse-table header")
	}
	parseCount := int(binary.LittleEndian.Uint32(data[8:12]))
	smallCount := int(binary.LittleEndian.Uint32(data[12:16]))
	if parseCount < 0 || smallCount < 0 ||
		parseCount > (len(data)-kotlinGeneratedTableHeaderBytes)/2 ||
		smallCount > (len(data)-kotlinGeneratedTableHeaderBytes)/2-parseCount ||
		len(data) != kotlinGeneratedTableHeaderBytes+2*(parseCount+smallCount) {
		panic("invalid generated Kotlin parse-table length")
	}

	parseTable := make([]uint16, parseCount)
	smallParseTable := make([]uint16, smallCount)
	offset := kotlinGeneratedTableHeaderBytes
	for _, table := range [][]uint16{parseTable, smallParseTable} {
		for index := range table {
			table[index] = binary.LittleEndian.Uint16(data[offset : offset+2])
			offset += 2
		}
	}
	return parseTable, smallParseTable
}
