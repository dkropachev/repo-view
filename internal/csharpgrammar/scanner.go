// Package csharpgrammar provides the generated C# grammar and its external
// scanner for the pure-Go Tree-sitter runtime.
package csharpgrammar

import (
	"sync"
	"unicode"

	treesitter "github.com/dcosson/treesitter-go"
)

// External token indexes must remain in the same order as grammar.js.
const (
	csharpOptionalSemi = iota
	csharpInterpolationRegularStart
	csharpInterpolationVerbatimStart
	csharpInterpolationRawStart
	csharpInterpolationStartQuote
	csharpInterpolationEndQuote
	csharpInterpolationOpenBrace
	csharpInterpolationCloseBrace
	csharpInterpolationStringContent
	csharpRawStringStart
	csharpRawStringEnd
	csharpRawStringContent
	csharpLambdaParenOpen
	csharpExternalTokenCount
)

const (
	// The upstream state format stores counts in one byte. Reject a longer
	// run instead of reproducing the C scanner's uint8 overflow behavior.
	csharpMaximumDelimiterRun = 255
	// Serialized interpolation frames use four bytes each. This cap stays
	// well below both the one-byte frame count and the runtime's 1024-byte
	// scanner-state buffer while bounding parser-owned scanner copies.
	csharpMaximumInterpolationDepth = 64
	// Raw and interpolated content scans can otherwise consume an entire
	// attacker-controlled source in one parser action.
	csharpMaximumExternalScanAdvances = 64 << 10
	// C# 14's simple-lambda probe speculatively scans through a complete
	// parameter list. Bound that independent ambiguity frontier tightly.
	csharpMaximumLambdaScanAdvances = 4 << 10
)

type csharpStringType uint8

const (
	csharpStringRegular csharpStringType = 1 << iota
	csharpStringVerbatim
	csharpStringRaw
)

type csharpInterpolation struct {
	dollarCount    uint8
	openBraceCount uint8
	quoteCount     uint8
	stringType     csharpStringType
}

func (interpolation csharpInterpolation) regular() bool {
	return interpolation.stringType&csharpStringRegular != 0
}

func (interpolation csharpInterpolation) verbatim() bool {
	return interpolation.stringType&csharpStringVerbatim != 0
}

func (interpolation csharpInterpolation) raw() bool {
	return interpolation.stringType&csharpStringRaw != 0
}

type csharpScanner struct {
	interpolations []csharpInterpolation
	quoteCount     uint8
}

var csharpLanguageOnce sync.Once
var csharpLanguage *treesitter.Language

// Language returns the generated C# language with a fresh external scanner
// factory. The returned parse tables are immutable after this assignment.
func Language() *treesitter.Language {
	csharpLanguageOnce.Do(func() {
		csharpLanguage = CSharpLanguage()
		csharpLanguage.NewExternalScanner = newCSharpScanner
	})
	return csharpLanguage
}

func newCSharpScanner() treesitter.ExternalScanner {
	return &csharpScanner{}
}

func (scanner *csharpScanner) Serialize(buffer []byte) uint32 {
	if scanner == nil || len(scanner.interpolations) > csharpMaximumInterpolationDepth {
		return 0
	}
	required := 2 + len(scanner.interpolations)*4
	if required > len(buffer) {
		return 0
	}
	buffer[0] = scanner.quoteCount
	buffer[1] = byte(len(scanner.interpolations))
	offset := 2
	for _, interpolation := range scanner.interpolations {
		buffer[offset] = interpolation.dollarCount
		buffer[offset+1] = interpolation.openBraceCount
		buffer[offset+2] = interpolation.quoteCount
		buffer[offset+3] = byte(interpolation.stringType)
		offset += 4
	}
	return uint32(required)
}

func (scanner *csharpScanner) Deserialize(data []byte) {
	if scanner == nil {
		return
	}
	scanner.quoteCount = 0
	scanner.interpolations = scanner.interpolations[:0]
	if len(data) == 0 {
		return
	}
	if len(data) < 2 {
		return
	}
	count := int(data[1])
	if count > csharpMaximumInterpolationDepth || len(data) != 2+count*4 {
		return
	}
	scanner.quoteCount = data[0]
	if cap(scanner.interpolations) < count {
		scanner.interpolations = make([]csharpInterpolation, 0, count)
	}
	offset := 2
	for range count {
		stringType := csharpStringType(data[offset+3])
		if stringType == 0 || stringType&^(csharpStringRegular|
			csharpStringVerbatim|csharpStringRaw) != 0 {
			scanner.quoteCount = 0
			scanner.interpolations = scanner.interpolations[:0]
			return
		}
		scanner.interpolations = append(scanner.interpolations, csharpInterpolation{
			dollarCount:    data[offset],
			openBraceCount: data[offset+1],
			quoteCount:     data[offset+2],
			stringType:     stringType,
		})
		offset += 4
	}
}

type csharpScanCursor struct {
	lexer     *treesitter.Lexer
	remaining int
	exhausted bool
}

func newCSharpScanCursor(lexer *treesitter.Lexer, limit int) csharpScanCursor {
	return csharpScanCursor{lexer: lexer, remaining: max(limit, 0)}
}

func (cursor *csharpScanCursor) advance(skip bool) bool {
	if cursor == nil || cursor.lexer == nil || cursor.remaining == 0 {
		if cursor != nil {
			cursor.exhausted = true
		}
		return false
	}
	cursor.remaining--
	cursor.lexer.Advance(skip)
	return true
}

func (cursor *csharpScanCursor) atEnd() bool {
	return cursor == nil || cursor.lexer == nil || cursor.lexer.EOF() ||
		cursor.lexer.Lookahead == 0
}

func (cursor *csharpScanCursor) skipWhitespace(skip bool) bool {
	for !cursor.atEnd() && unicode.IsSpace(cursor.lexer.Lookahead) {
		if !cursor.advance(skip) {
			return false
		}
	}
	return true
}

func csharpValidExternalSymbol(validSymbols []bool, symbol int) bool {
	return symbol >= 0 && symbol < len(validSymbols) && validSymbols[symbol]
}

func (scanner *csharpScanner) Scan(
	lexer *treesitter.Lexer,
	validSymbols []bool,
) bool {
	if scanner == nil || lexer == nil || len(validSymbols) < csharpExternalTokenCount {
		return false
	}

	if csharpValidExternalSymbol(validSymbols, csharpLambdaParenOpen) {
		cursor := newCSharpScanCursor(lexer, csharpMaximumLambdaScanAdvances)
		switch csharpScanLambdaParenOpen(&cursor) {
		case csharpLambdaScanSuccess:
			return true
		case csharpLambdaScanFailed:
			return false
		case csharpLambdaScanNoParen:
		}
	}

	cursor := newCSharpScanCursor(lexer, csharpMaximumExternalScanAdvances)
	if csharpValidExternalSymbol(validSymbols, csharpOptionalSemi) &&
		csharpValidExternalSymbol(validSymbols, csharpInterpolationRegularStart) {
		// This combination is an error-recovery state. Matching either token
		// here produces worse and less stable recovery trees upstream.
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpOptionalSemi) {
		lexer.ResultSymbol = treesitter.Symbol(csharpOptionalSemi)
		if lexer.Lookahead == ';' && !cursor.advance(false) {
			return false
		}
		return true
	}

	didAdvance := false
	if csharpValidExternalSymbol(validSymbols, csharpRawStringStart) &&
		scanner.scanRawStringStart(&cursor) {
		return true
	}
	if cursor.exhausted {
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpRawStringEnd) {
		matched, advanced := scanner.scanRawStringEnd(&cursor)
		if matched {
			return true
		}
		didAdvance = advanced
	}
	if cursor.exhausted {
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpRawStringContent) {
		return scanner.scanRawStringContent(&cursor)
	}

	if csharpValidExternalSymbol(validSymbols, csharpInterpolationRegularStart) ||
		csharpValidExternalSymbol(validSymbols, csharpInterpolationVerbatimStart) ||
		csharpValidExternalSymbol(validSymbols, csharpInterpolationRawStart) {
		if scanner.scanInterpolationStart(&cursor) {
			return true
		}
		if cursor.exhausted {
			return false
		}
	}
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationStartQuote) &&
		scanner.scanInterpolationStartQuote(&cursor) {
		return true
	}
	if cursor.exhausted {
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationEndQuote) {
		matched, advanced := scanner.scanInterpolationEndQuote(&cursor)
		if matched {
			return true
		}
		didAdvance = didAdvance || advanced
	}
	if cursor.exhausted {
		return false
	}
	braceAdvanced := 0
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationOpenBrace) {
		matched, handled := scanner.scanRawInterpolationBraceRun(
			&cursor,
			csharpValidExternalSymbol(validSymbols, csharpInterpolationStringContent),
		)
		if handled {
			return matched
		}
		matched, advanced := scanner.scanInterpolationOpenBrace(&cursor)
		if matched {
			return true
		}
		braceAdvanced = advanced
	}
	if cursor.exhausted {
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationStringContent) {
		matched, handled := scanner.scanRawInterpolationClosingContentRun(&cursor)
		if handled {
			return matched
		}
	}
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationCloseBrace) &&
		scanner.scanInterpolationCloseBrace(&cursor) {
		return true
	}
	if cursor.exhausted {
		return false
	}
	if csharpValidExternalSymbol(validSymbols, csharpInterpolationStringContent) {
		return scanner.scanInterpolationStringContent(
			&cursor,
			didAdvance,
			braceAdvanced,
		)
	}
	return false
}

func (scanner *csharpScanner) scanRawStringStart(cursor *csharpScanCursor) bool {
	if !cursor.skipWhitespace(true) || cursor.atEnd() || cursor.lexer.Lookahead != '"' {
		return false
	}
	quoteCount, ok := csharpConsumeRun(cursor, '"')
	if !ok || quoteCount < 3 {
		return false
	}
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpRawStringStart)
	scanner.quoteCount = uint8(quoteCount)
	return true
}

func (scanner *csharpScanner) scanRawStringEnd(
	cursor *csharpScanCursor,
) (matched, advanced bool) {
	if cursor.atEnd() || cursor.lexer.Lookahead != '"' {
		return false, false
	}
	quoteCount, ok := csharpConsumeRun(cursor, '"')
	if !ok {
		return false, quoteCount > 0
	}
	if quoteCount == int(scanner.quoteCount) {
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpRawStringEnd)
		scanner.quoteCount = 0
		return true, true
	}
	return false, quoteCount > 0
}

func (scanner *csharpScanner) scanRawStringContent(
	cursor *csharpScanCursor,
) bool {
	for !cursor.atEnd() {
		if cursor.lexer.Lookahead == '"' {
			cursor.lexer.MarkEnd()
			quoteCount, ok := csharpConsumeRun(cursor, '"')
			if !ok {
				return false
			}
			if quoteCount == int(scanner.quoteCount) {
				cursor.lexer.ResultSymbol = treesitter.Symbol(csharpRawStringContent)
				return true
			}
		}
		if !cursor.advance(false) {
			return false
		}
	}
	cursor.lexer.MarkEnd()
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpRawStringContent)
	// The upstream scanner deliberately emits an empty content token at EOF.
	return true
}

func (scanner *csharpScanner) scanInterpolationStart(cursor *csharpScanCursor) bool {
	if !cursor.skipWhitespace(true) || cursor.atEnd() {
		return false
	}
	isVerbatim := false
	if cursor.lexer.Lookahead == '@' {
		isVerbatim = true
		if !cursor.advance(false) {
			return false
		}
	}

	dollarCount, ok := csharpConsumeRun(cursor, '$')
	if !ok || dollarCount == 0 ||
		(cursor.lexer.Lookahead != '"' && cursor.lexer.Lookahead != '@') {
		return false
	}
	interpolation := csharpInterpolation{dollarCount: uint8(dollarCount)}
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationRegularStart)
	if isVerbatim || cursor.lexer.Lookahead == '@' {
		if cursor.lexer.Lookahead == '@' {
			if !cursor.advance(false) {
				return false
			}
			isVerbatim = true
		}
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationVerbatimStart)
		interpolation.stringType = csharpStringVerbatim
	}

	if cursor.lexer.Lookahead != '"' {
		return false
	}
	cursor.lexer.MarkEnd()
	if !cursor.advance(false) {
		return false
	}
	push := true
	if cursor.lexer.Lookahead == '"' && !isVerbatim {
		if !cursor.advance(false) {
			return false
		}
		if cursor.lexer.Lookahead == '"' {
			cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationRawStart)
			interpolation.stringType |= csharpStringRaw
		} else {
			// Exactly two quotes form an empty ordinary interpolated string.
			push = false
		}
	} else {
		interpolation.stringType |= csharpStringRegular
	}
	if push {
		if len(scanner.interpolations) == csharpMaximumInterpolationDepth {
			return false
		}
		scanner.interpolations = append(scanner.interpolations, interpolation)
	}
	return true
}

func (scanner *csharpScanner) scanInterpolationStartQuote(
	cursor *csharpScanCursor,
) bool {
	current := scanner.currentInterpolation()
	if current == nil {
		return false
	}
	quoteCount := 0
	if current.verbatim() || current.regular() {
		if cursor.lexer.Lookahead == '"' {
			if !cursor.advance(false) {
				return false
			}
			quoteCount = 1
		}
	} else {
		var ok bool
		quoteCount, ok = csharpConsumeRun(cursor, '"')
		if !ok {
			return false
		}
	}
	if quoteCount == 0 || int(current.quoteCount)+quoteCount > csharpMaximumDelimiterRun {
		return false
	}
	current.quoteCount += uint8(quoteCount)
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationStartQuote)
	return true
}

func (scanner *csharpScanner) scanInterpolationEndQuote(
	cursor *csharpScanCursor,
) (matched, advanced bool) {
	current := scanner.currentInterpolation()
	if current == nil {
		return false, false
	}
	quoteCount, ok := csharpConsumeRun(cursor, '"')
	if !ok {
		return false, quoteCount > 0
	}
	if quoteCount == int(current.quoteCount) {
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationEndQuote)
		scanner.interpolations = scanner.interpolations[:len(scanner.interpolations)-1]
		return true, quoteCount > 0
	}
	return false, quoteCount > 0
}

func (scanner *csharpScanner) scanInterpolationOpenBrace(
	cursor *csharpScanCursor,
) (matched bool, advanced int) {
	current := scanner.currentInterpolation()
	if current == nil {
		return false, 0
	}
	braceCount := 0
	for cursor.lexer.Lookahead == '{' && braceCount < int(current.dollarCount) {
		if !cursor.advance(false) {
			return false, braceCount
		}
		braceCount++
	}
	if braceCount == 0 || braceCount != int(current.dollarCount) ||
		cursor.lexer.Lookahead == '{' {
		return false, braceCount
	}
	current.openBraceCount = uint8(braceCount)
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationOpenBrace)
	return true, braceCount
}

// scanRawInterpolationBraceRun disambiguates a raw interpolated string's
// opening-brace run before the generic scanner consumes its delimiter prefix.
// For N dollar signs, fewer than N braces are content, exactly N start an
// interpolation, N+1 through 2N-1 contain a literal prefix followed by an
// interpolation, and 2N or more are invalid. For the mixed case, emit one
// literal brace and leave the lexer marked there; repeated scans peel the
// complete literal prefix until exactly N braces remain.
func (scanner *csharpScanner) scanRawInterpolationBraceRun(
	cursor *csharpScanCursor,
	contentValid bool,
) (matched, handled bool) {
	current := scanner.currentInterpolation()
	if current == nil || !current.raw() || current.openBraceCount != 0 ||
		cursor.atEnd() || cursor.lexer.Lookahead != '{' {
		return false, false
	}
	delimiter := int(current.dollarCount)
	if delimiter == 0 {
		return false, true
	}

	runLimit := delimiter * 2
	braceCount := 0
	for cursor.lexer.Lookahead == '{' && braceCount < runLimit {
		if !cursor.advance(false) {
			return false, true
		}
		braceCount++
		if braceCount == 1 {
			// Preserve the one-brace content token used when a valid run has a
			// literal prefix. Later MarkEnd calls replace this for shorter and
			// exact-delimiter runs.
			cursor.lexer.MarkEnd()
		}
	}

	switch {
	case braceCount < delimiter:
		if !contentValid {
			return false, true
		}
		cursor.lexer.MarkEnd()
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationStringContent)
		return true, true
	case braceCount == delimiter:
		cursor.lexer.MarkEnd()
		current.openBraceCount = uint8(braceCount)
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationOpenBrace)
		return true, true
	case braceCount < runLimit:
		if !contentValid {
			return false, true
		}
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationStringContent)
		return true, true
	default:
		return false, true
	}
}

// scanRawInterpolationClosingContentRun handles closing braces while the raw
// string is outside an interpolation. Only runs shorter than the dollar-sign
// delimiter are literal content; a run at least as long as the delimiter is
// an unmatched interpolation terminator and must remain a parse error.
func (scanner *csharpScanner) scanRawInterpolationClosingContentRun(
	cursor *csharpScanCursor,
) (matched, handled bool) {
	current := scanner.currentInterpolation()
	if current == nil || !current.raw() || current.openBraceCount != 0 ||
		cursor.atEnd() || cursor.lexer.Lookahead != '}' {
		return false, false
	}
	delimiter := int(current.dollarCount)
	if delimiter == 0 {
		return false, true
	}

	braceCount := 0
	for cursor.lexer.Lookahead == '}' && braceCount < delimiter {
		if !cursor.advance(false) {
			return false, true
		}
		braceCount++
	}
	if braceCount == delimiter {
		return false, true
	}
	cursor.lexer.MarkEnd()
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationStringContent)
	return true, true
}

func (scanner *csharpScanner) scanInterpolationCloseBrace(cursor *csharpScanCursor) bool {
	current := scanner.currentInterpolation()
	if current == nil || !cursor.skipWhitespace(false) {
		return false
	}
	if current.raw() {
		delimiter := int(current.openBraceCount)
		if delimiter == 0 {
			return false
		}
		runLimit := delimiter * 2
		braceCount := 0
		for cursor.lexer.Lookahead == '}' && braceCount < runLimit {
			if !cursor.advance(false) {
				return false
			}
			braceCount++
			if braceCount == delimiter {
				// Preserve the valid interpolation endpoint while probing far
				// enough to reject the raw-string-invalid 2N closing run.
				cursor.lexer.MarkEnd()
			}
		}
		if braceCount < delimiter || braceCount == runLimit {
			return false
		}
		current.openBraceCount = 0
		cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationCloseBrace)
		return true
	}
	braceCount := 0
	for cursor.lexer.Lookahead == '}' {
		if braceCount == csharpMaximumDelimiterRun || !cursor.advance(false) {
			return false
		}
		braceCount++
		if braceCount == int(current.openBraceCount) {
			current.openBraceCount = 0
			cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationCloseBrace)
			return true
		}
	}
	return false
}

func (scanner *csharpScanner) scanInterpolationStringContent(
	cursor *csharpScanCursor,
	didAdvance bool,
	braceCount int,
) bool {
	current := scanner.currentInterpolation()
	if current == nil {
		return false
	}
	cursor.lexer.ResultSymbol = treesitter.Symbol(csharpInterpolationStringContent)
	// A failed one-brace interpolation probe has already consumed the first
	// brace in a regular or verbatim `{{` escape. Finish exactly that escaped
	// pair and mark its end. If another brace follows, tree-sitter restarts at
	// it, allowing `{{{value}}}` to become literal `{` plus an interpolation
	// instead of one undifferentiated content token.
	if !current.raw() && current.dollarCount == 1 && braceCount == 1 &&
		cursor.lexer.Lookahead == '{' {
		if !cursor.advance(false) {
			return false
		}
		cursor.lexer.MarkEnd()
		return true
	}
	for !cursor.atEnd() {
		switch {
		case current.raw():
			if cursor.lexer.Lookahead == '"' {
				cursor.lexer.MarkEnd()
				if !cursor.advance(false) {
					return false
				}
				if cursor.lexer.Lookahead == '"' {
					if !cursor.advance(false) {
						return false
					}
					quoteCount := 2
					for cursor.lexer.Lookahead == '"' {
						if quoteCount == csharpMaximumDelimiterRun ||
							!cursor.advance(false) {
							return false
						}
						quoteCount++
					}
					if quoteCount == int(current.quoteCount) {
						return didAdvance
					}
				}
			}
			if cursor.lexer.Lookahead == '{' {
				cursor.lexer.MarkEnd()
				for cursor.lexer.Lookahead == '{' &&
					braceCount < int(current.openBraceCount) {
					if !cursor.advance(false) {
						return false
					}
					braceCount++
				}
				if braceCount == int(current.openBraceCount) &&
					(braceCount == 0 || cursor.lexer.Lookahead != '{') {
					return didAdvance
				}
			}
			if current.openBraceCount == 0 && cursor.lexer.Lookahead == '}' {
				cursor.lexer.MarkEnd()
				return didAdvance
			}

		case current.verbatim():
			if cursor.lexer.Lookahead == '"' {
				cursor.lexer.MarkEnd()
				if !cursor.advance(false) {
					return false
				}
				if cursor.lexer.Lookahead == '"' {
					if !cursor.advance(false) {
						return false
					}
					continue
				}
				return didAdvance
			}
			if cursor.lexer.Lookahead == '{' {
				cursor.lexer.MarkEnd()
				for cursor.lexer.Lookahead == '{' &&
					braceCount < int(current.openBraceCount) {
					if !cursor.advance(false) {
						return false
					}
					braceCount++
				}
				if braceCount == int(current.openBraceCount) &&
					(braceCount == 0 || cursor.lexer.Lookahead != '{') {
					return didAdvance
				}
			}

		case current.regular():
			if cursor.lexer.Lookahead == '\\' || cursor.lexer.Lookahead == '\n' ||
				cursor.lexer.Lookahead == '"' {
				cursor.lexer.MarkEnd()
				return didAdvance
			}
			if cursor.lexer.Lookahead == '{' {
				cursor.lexer.MarkEnd()
				for cursor.lexer.Lookahead == '{' &&
					braceCount < int(current.openBraceCount) {
					if !cursor.advance(false) {
						return false
					}
					braceCount++
				}
				if braceCount == int(current.openBraceCount) &&
					(braceCount == 0 || cursor.lexer.Lookahead != '{') {
					return didAdvance
				}
			}
		}

		if cursor.lexer.Lookahead != '{' {
			braceCount = 0
		}
		if !cursor.advance(false) {
			return false
		}
		didAdvance = true
	}
	cursor.lexer.MarkEnd()
	return didAdvance
}

func (scanner *csharpScanner) currentInterpolation() *csharpInterpolation {
	if scanner == nil || len(scanner.interpolations) == 0 {
		return nil
	}
	return &scanner.interpolations[len(scanner.interpolations)-1]
}

func csharpConsumeRun(
	cursor *csharpScanCursor,
	want int32,
) (int, bool) {
	count := 0
	for cursor != nil && cursor.lexer != nil && cursor.lexer.Lookahead == want {
		if count == csharpMaximumDelimiterRun || !cursor.advance(false) {
			return count, false
		}
		count++
	}
	return count, true
}

type csharpLambdaScanResult uint8

const (
	csharpLambdaScanNoParen csharpLambdaScanResult = iota
	csharpLambdaScanFailed
	csharpLambdaScanSuccess
)

func csharpScanLambdaParenOpen(cursor *csharpScanCursor) csharpLambdaScanResult {
	if !cursor.skipWhitespace(true) || cursor.exhausted {
		return csharpLambdaScanFailed
	}
	if cursor.atEnd() || cursor.lexer.Lookahead != '(' {
		return csharpLambdaScanNoParen
	}
	if !cursor.advance(false) {
		return csharpLambdaScanFailed
	}
	cursor.lexer.MarkEnd()

	sawHardModifier := false
	expectingElement := true
	for {
		if !csharpSkipLambdaWhitespaceAndComments(cursor) || cursor.atEnd() {
			return csharpLambdaScanFailed
		}
		if cursor.lexer.Lookahead == ')' {
			if !cursor.advance(false) ||
				!csharpSkipLambdaWhitespaceAndComments(cursor) ||
				!sawHardModifier || cursor.lexer.Lookahead != '=' ||
				!cursor.advance(false) || cursor.lexer.Lookahead != '>' {
				return csharpLambdaScanFailed
			}
			cursor.lexer.ResultSymbol = treesitter.Symbol(csharpLambdaParenOpen)
			return csharpLambdaScanSuccess
		}
		if !expectingElement {
			if cursor.lexer.Lookahead != ',' || !cursor.advance(false) {
				return csharpLambdaScanFailed
			}
			expectingElement = true
			continue
		}

		consumedName := false
		for !consumedName {
			if !csharpSkipLambdaWhitespaceAndComments(cursor) ||
				!csharpLambdaIdentifierStart(cursor.lexer.Lookahead) {
				return csharpLambdaScanFailed
			}
			identifier, length, ascii, ok := csharpConsumeLambdaIdentifier(cursor)
			if !ok {
				return csharpLambdaScanFailed
			}
			hardModifier := ascii && length <= len(identifier) &&
				csharpLambdaHardModifier(string(identifier[:length]))
			softModifier := ascii && length == len("scoped") &&
				string(identifier[:length]) == "scoped"
			switch {
			case hardModifier:
				sawHardModifier = true
			case softModifier:
			default:
				consumedName = true
			}
		}
		expectingElement = false
	}
}

func csharpSkipLambdaWhitespaceAndComments(cursor *csharpScanCursor) bool {
	for {
		if !cursor.skipWhitespace(false) || cursor.atEnd() {
			return !cursor.exhausted
		}
		if cursor.lexer.Lookahead != '/' {
			return true
		}
		if !cursor.advance(false) {
			return false
		}
		switch cursor.lexer.Lookahead {
		case '/':
			for !cursor.atEnd() && cursor.lexer.Lookahead != '\n' {
				if !cursor.advance(false) {
					return false
				}
			}
		case '*':
			if !cursor.advance(false) {
				return false
			}
			previous := int32(0)
			for !cursor.atEnd() && (previous != '*' || cursor.lexer.Lookahead != '/') {
				previous = cursor.lexer.Lookahead
				if !cursor.advance(false) {
					return false
				}
			}
			if cursor.lexer.Lookahead == '/' && !cursor.advance(false) {
				return false
			}
		default:
			// The slash was consumed just like the upstream speculative scan;
			// its caller will fail the surrounding lambda and rewind the lexer.
			return true
		}
	}
}

func csharpConsumeLambdaIdentifier(
	cursor *csharpScanCursor,
) (identifier [8]byte, length int, ascii, ok bool) {
	ascii = true
	if cursor == nil || cursor.lexer == nil || cursor.atEnd() {
		return identifier, length, ascii, false
	}
	if cursor.lexer.Lookahead == '@' {
		if !csharpConsumeLambdaIdentifierCharacter(
			cursor, &identifier, &length, &ascii,
		) {
			return identifier, length, ascii, false
		}
	}
	switch cursor.lexer.Lookahead {
	case '\\':
		if !csharpConsumeLambdaIdentifierEscape(
			cursor, &identifier, &length, &ascii,
		) {
			return identifier, length, ascii, false
		}
	default:
		if !csharpLambdaXIDStart(cursor.lexer.Lookahead) {
			return identifier, length, ascii, false
		}
		if !csharpConsumeLambdaIdentifierCharacter(
			cursor, &identifier, &length, &ascii,
		) {
			return identifier, length, ascii, false
		}
	}
	for !cursor.atEnd() {
		if cursor.lexer.Lookahead == '\\' {
			if !csharpConsumeLambdaIdentifierEscape(
				cursor, &identifier, &length, &ascii,
			) {
				return identifier, length, ascii, false
			}
			continue
		}
		if !csharpLambdaIdentifierContinue(cursor.lexer.Lookahead) ||
			!csharpConsumeLambdaIdentifierCharacter(
				cursor, &identifier, &length, &ascii,
			) {
			break
		}
	}
	return identifier, length, ascii, true
}

func csharpConsumeLambdaIdentifierEscape(
	cursor *csharpScanCursor,
	identifier *[8]byte,
	length *int,
	ascii *bool,
) bool {
	if cursor == nil || cursor.lexer == nil || cursor.lexer.Lookahead != '\\' ||
		!csharpConsumeLambdaIdentifierCharacter(cursor, identifier, length, ascii) {
		return false
	}
	digits := 4
	if cursor.lexer.Lookahead == 'U' {
		digits = 8
	} else if cursor.lexer.Lookahead != 'u' {
		return false
	}
	if !csharpConsumeLambdaIdentifierCharacter(cursor, identifier, length, ascii) {
		return false
	}
	for range digits {
		if !csharpLambdaHexDigit(cursor.lexer.Lookahead) ||
			!csharpConsumeLambdaIdentifierCharacter(
				cursor, identifier, length, ascii,
			) {
			return false
		}
	}
	return true
}

func csharpConsumeLambdaIdentifierCharacter(
	cursor *csharpScanCursor,
	identifier *[8]byte,
	length *int,
	ascii *bool,
) bool {
	if cursor == nil || cursor.lexer == nil || identifier == nil ||
		length == nil || ascii == nil || cursor.atEnd() {
		return false
	}
	character := cursor.lexer.Lookahead
	if character < 0 || character > unicode.MaxASCII {
		*ascii = false
	} else if *length < len(identifier) {
		identifier[*length] = byte(character)
	}
	*length++
	return cursor.advance(false)
}

func csharpLambdaHexDigit(character int32) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}

func csharpLambdaHardModifier(identifier string) bool {
	switch identifier {
	case "ref", "out", "in", "readonly":
		return true
	default:
		return false
	}
}

func csharpLambdaIdentifierStart(character int32) bool {
	return character == '@' || character == '\\' || csharpLambdaXIDStart(character)
}

func csharpLambdaIdentifierContinue(character int32) bool {
	if csharpLambdaXIDStart(character) {
		return true
	}
	return character >= 0 && (unicode.Is(unicode.Mn, character) ||
		unicode.Is(unicode.Mc, character) || unicode.Is(unicode.Nd, character) ||
		unicode.Is(unicode.Pc, character) ||
		unicode.Is(unicode.Other_ID_Continue, character))
}

// Go's standard library does not expose the derived XID tables directly.
// XID_Start is a subset of these ID_Start categories; accepting the superset
// is safe for this speculative external token because the generated grammar
// still validates the identifier token itself.
func csharpLambdaXIDStart(character int32) bool {
	if character == '_' {
		return true
	}
	return character >= 0 && (unicode.IsLetter(character) ||
		unicode.Is(unicode.Nl, character) || unicode.Is(unicode.Other_ID_Start, character))
}
