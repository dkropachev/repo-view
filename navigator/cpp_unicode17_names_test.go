//nolint:misspell // Unicode tests preserve official character names such as TJE.
package navigator

import (
	"strings"
	"testing"
)

func TestCPPUnicode17ExplicitNamedUCNDelta(t *testing.T) {
	t.Parallel()

	tests := map[string]rune{
		"ARABIC PEPET":                                     '\u0897',
		"CYRILLIC CAPITAL LETTER TJE":                      '\u1C89',
		"CYRILLIC SMALL LETTER TJE":                        '\u1C8A',
		"TODHRI LETTER A":                                  '\U000105C0',
		"KHITAN SMALL SCRIPT CHARACTER-18CFF":              '\U00018CFF',
		"TANGUT COMPONENT-769":                             '\U00018D80',
		"TOLONG SIKI LETTER I":                             '\U00011DB0',
		"TAI YO LETTER LOW KO":                             '\U0001E6C0',
		"CUNEIFORM SIGN KALAM":                             '\U00012327',
		"BAMUM LETTER PHASE-B PUNGGAAM":                    '\U00016881',
		"MENDE KIKAKUI SYLLABLE M172 MBO":                  '\U0001E899',
		"LATIN CAPITAL LETTER PHARYNGEAL VOICED FRICATIVE": '\uA7CE',
	}
	for name, want := range tests {
		got, ok := cppUnicode17ExplicitNamedUCNRune(name)
		if !ok || got != want {
			t.Errorf("Unicode 17 explicit name %q = %U, %t; want %U, true",
				name, got, ok, want)
		}
	}

	for _, name := range []string{
		"LATIN CAPITAL LETTER A",
		"EGYPTIAN HIEROGLYPH-13460",
		"CJK UNIFIED IDEOGRAPH-2EBF0",
		"cyrillic capital letter tje",
		"CYRILLIC CAPITAL LETTER MADE UP",
	} {
		if got, ok := cppUnicode17ExplicitNamedUCNRune(name); ok {
			t.Errorf("non-delta name %q resolved to %U", name, got)
		}
	}
}

func TestCPPUnicode17AlgorithmicNamedUCNsUseAssignedRanges(t *testing.T) {
	t.Parallel()

	tests := map[string]rune{
		"CJK UNIFIED IDEOGRAPH-4E00":  '\u4E00',
		"CJK UNIFIED IDEOGRAPH-2B73A": '\U0002B73A',
		"CJK UNIFIED IDEOGRAPH-2CEA2": '\U0002CEA2',
		"CJK UNIFIED IDEOGRAPH-2EBF0": '\U0002EBF0',
		"CJK UNIFIED IDEOGRAPH-2EE5D": '\U0002EE5D',
		"CJK UNIFIED IDEOGRAPH-323B0": '\U000323B0',
		"CJK UNIFIED IDEOGRAPH-33479": '\U00033479',
		"TANGUT IDEOGRAPH-17000":      '\U00017000',
		"TANGUT IDEOGRAPH-187F8":      '\U000187F8',
		"TANGUT IDEOGRAPH-187FF":      '\U000187FF',
		"TANGUT IDEOGRAPH-18D09":      '\U00018D09',
		"TANGUT IDEOGRAPH-18D1E":      '\U00018D1E',
		"EGYPTIAN HIEROGLYPH-13460":   '\U00013460',
		"EGYPTIAN HIEROGLYPH-143FA":   '\U000143FA',
	}
	for name, want := range tests {
		got, ok := cppUnicode17AlgorithmicNamedUCNRune(name)
		if !ok || got != want {
			t.Errorf("Unicode 17 algorithmic name %q = %U, %t; want %U, true",
				name, got, ok, want)
		}
	}

	for _, name := range []string{
		"CJK UNIFIED IDEOGRAPH-04E00",
		"CJK UNIFIED IDEOGRAPH-2ebf0",
		"CJK UNIFIED IDEOGRAPH-2EBE1",
		"CJK UNIFIED IDEOGRAPH-2EE5E",
		"CJK UNIFIED IDEOGRAPH-3134B",
		"CJK UNIFIED IDEOGRAPH-3347A",
		"TANGUT IDEOGRAPH-18800",
		"TANGUT IDEOGRAPH-18D1F",
		"EGYPTIAN HIEROGLYPH-1345F",
		"EGYPTIAN HIEROGLYPH-143FB",
		"EGYPTIAN HIEROGLYPH-1346a",
		"CJK UNIFIED IDEOGRAPH-110000",
		"CJK UNIFIED IDEOGRAPH-",
	} {
		if got, ok := cppUnicode17AlgorithmicNamedUCNRune(name); ok {
			t.Errorf("unassigned or noncanonical name %q resolved to %U", name, got)
		}
	}
}

func TestCPPUnicode17NamedUCNGeneratedDataInvariants(t *testing.T) {
	t.Parallel()

	if cppUnicodeNamedUCNVersion != "17.0.0" {
		t.Fatalf("generated Unicode name version = %q, want 17.0.0",
			cppUnicodeNamedUCNVersion)
	}
	if got := len(cppUnicode17ExplicitNamedUCNs); got != 799 {
		t.Fatalf("explicit Unicode name delta entries = %d, want 799", got)
	}

	previous := ""
	for index, entry := range cppUnicode17ExplicitNamedUCNs {
		if index > 0 && entry.name <= previous {
			t.Fatalf("generated names are not strictly sorted at %d: %q then %q",
				index, previous, entry.name)
		}
		if len(entry.name) > cppMaximumNamedUCNBytes {
			t.Fatalf("generated name %q has %d bytes, cap %d",
				entry.name, len(entry.name), cppMaximumNamedUCNBytes)
		}
		for _, character := range entry.name {
			if character != ' ' && character != '-' &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
				t.Fatalf("generated name %q contains invalid character %q",
					entry.name, character)
			}
		}
		if !cIdentifierRune(entry.character, false) {
			t.Fatalf("generated name %q maps to non-XID_Continue %U",
				entry.name, entry.character)
		}
		if strings.HasPrefix(entry.name, "EGYPTIAN HIEROGLYPH-") {
			t.Fatalf("algorithmic Egyptian name leaked into explicit table: %q",
				entry.name)
		}
		if got, ok := cppUnicode17ExplicitNamedUCNRune(entry.name); !ok || got != entry.character {
			t.Fatalf("generated lookup %q = %U, %t; want %U, true",
				entry.name, got, ok, entry.character)
		}
		previous = entry.name
	}

	for _, ranges := range [][]cppUnicode17NameRange{
		cppUnicode17CJKUnifiedIdeographRanges[:],
		cppUnicode17TangutIdeographRanges[:],
		cppUnicode17EgyptianHieroglyphRanges[:],
	} {
		for index, characterRange := range ranges {
			if characterRange.first > characterRange.last {
				t.Fatalf("generated range %d is reversed: %#v", index, characterRange)
			}
			if index > 0 && ranges[index-1].last >= characterRange.first {
				t.Fatalf("generated ranges overlap or are unsorted: %#v", ranges)
			}
			if !cIdentifierRune(characterRange.first, false) ||
				!cIdentifierRune(characterRange.last, false) {
				t.Fatalf("generated range boundary is not XID_Continue: %#v",
					characterRange)
			}
		}
	}
}

func TestCPPUnicode17GeneratedNameLookupDoesNotAllocate(t *testing.T) {
	var explicitCharacter, algorithmicCharacter rune
	var explicitOK, algorithmicOK bool
	allocations := testing.AllocsPerRun(100, func() {
		explicitCharacter, explicitOK = cppUnicode17ExplicitNamedUCNRune(
			"CYRILLIC CAPITAL LETTER TJE",
		)
		algorithmicCharacter, algorithmicOK = cppUnicode17AlgorithmicNamedUCNRune(
			"CJK UNIFIED IDEOGRAPH-323B0",
		)
	})
	if allocations != 0 {
		t.Fatalf("generated Unicode name lookup allocated %.2f objects, want zero",
			allocations)
	}
	if !explicitOK || explicitCharacter != '\u1C89' ||
		!algorithmicOK || algorithmicCharacter != '\U000323B0' {
		t.Fatalf("generated lookup results = (%U, %t), (%U, %t)",
			explicitCharacter, explicitOK, algorithmicCharacter, algorithmicOK)
	}
}

func TestCPPNamedUniversalCharactersUseUnicode17IndependentOfGoTables(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]rune{
		"CYRILLIC CAPITAL LETTER TJE":         '\u1C89',
		"KHITAN SMALL SCRIPT CHARACTER-18CFF": '\U00018CFF',
		"TOLONG SIKI LETTER I":                '\U00011DB0',
		"TANGUT COMPONENT-769":                '\U00018D80',
		"CUNEIFORM SIGN KALAM":                '\U00012327',
		"CJK UNIFIED IDEOGRAPH-2EBF0":         '\U0002EBF0',
		"CJK UNIFIED IDEOGRAPH-323B0":         '\U000323B0',
		"TANGUT IDEOGRAPH-187F8":              '\U000187F8',
		"EGYPTIAN HIEROGLYPH-13460":           '\U00013460',
	} {
		got, ok := cppNamedUCNRune(name)
		if !ok || got != want {
			t.Errorf("integrated Unicode 17 name %q = %U, %t; want %U, true",
				name, got, ok, want)
		}
	}
}

func TestCPPUnicode17LiteralNumericAndNamedIdentifierFormsAgree(t *testing.T) {
	t.Parallel()

	for _, identifier := range []string{
		"\u1C89value",
		`\u{1C89}value`,
		`\N{CYRILLIC CAPITAL LETTER TJE}value`,
	} {
		if !cppSourceIdentifier(identifier) {
			t.Errorf("Unicode 16 identifier-start form %q was rejected", identifier)
		}
	}

	for _, identifier := range []string{
		"a\u0897",
		`a\u{897}`,
		`a\N{ARABIC PEPET}`,
	} {
		if !cppSourceIdentifier(identifier) {
			t.Errorf("Unicode 16 identifier-continue form %q was rejected", identifier)
		}
	}
	for _, identifier := range []string{
		"\u0897value",
		`\u{897}value`,
		`\N{ARABIC PEPET}value`,
	} {
		if cppSourceIdentifier(identifier) {
			t.Errorf("identifier-continue-only form %q was accepted at start", identifier)
		}
	}
}
