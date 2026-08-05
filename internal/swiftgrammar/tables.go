package swiftgrammar

import (
	_ "embed"
	"encoding/binary"
)

const (
	swiftGeneratedTableMagic       = "RVSWIFT!"
	swiftGeneratedTableHeaderBytes = 16
)

//go:embed language_tables.bin
var swiftGeneratedTableData []byte

func swiftGeneratedParseTables() ([]uint16, []uint16) {
	data := swiftGeneratedTableData
	if len(data) < swiftGeneratedTableHeaderBytes ||
		string(data[:8]) != swiftGeneratedTableMagic {
		panic("invalid generated Swift parse-table header")
	}
	parseCount := int(binary.LittleEndian.Uint32(data[8:12]))
	smallCount := int(binary.LittleEndian.Uint32(data[12:16]))
	if parseCount < 0 || smallCount < 0 ||
		parseCount > (len(data)-swiftGeneratedTableHeaderBytes)/2 ||
		smallCount > (len(data)-swiftGeneratedTableHeaderBytes)/2-parseCount ||
		len(data) != swiftGeneratedTableHeaderBytes+2*(parseCount+smallCount) {
		panic("invalid generated Swift parse-table length")
	}

	parseTable := make([]uint16, parseCount)
	smallParseTable := make([]uint16, smallCount)
	offset := swiftGeneratedTableHeaderBytes
	for _, table := range [][]uint16{parseTable, smallParseTable} {
		for index := range table {
			table[index] = binary.LittleEndian.Uint16(data[offset : offset+2])
			offset += 2
		}
	}
	return parseTable, smallParseTable
}
