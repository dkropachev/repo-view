// Package swiftgrammar provides the generated Swift grammar and its bounded
// pure-Go external scanner.
package swiftgrammar

import (
	"encoding/binary"
	"sync"
	"unicode"

	treesitter "github.com/dcosson/treesitter-go"
)

// External token indexes must remain in the exact order of the pinned
// tree-sitter-swift grammar's externals array.
const (
	swiftBlockComment = iota
	swiftRawStringPart
	swiftRawStringContinuingIndicator
	swiftRawStringEndPart
	swiftImplicitSemicolon
	swiftExplicitSemicolon
	swiftArrowOperator
	swiftDotOperator
	swiftConjunctionOperator
	swiftDisjunctionOperator
	swiftNilCoalescingOperator
	swiftEqualSign
	swiftEqualEqual
	swiftPlusThenWhitespace
	swiftMinusThenWhitespace
	swiftBang
	swiftThrowsKeyword
	swiftRethrowsKeyword
	swiftDefaultKeyword
	swiftWhereKeyword
	swiftElseKeyword
	swiftCatchKeyword
	swiftAsKeyword
	swiftAsQuestion
	swiftAsBang
	swiftAsyncKeyword
	swiftCustomOperator
	swiftHashSymbol
	swiftDirectiveIf
	swiftDirectiveElseIf
	swiftDirectiveElse
	swiftDirectiveEndIf
	swiftFakeTryBang
	swiftExternalTokenCount
)

const (
	// Comments, raw strings, whitespace, and custom operators can otherwise
	// consume an entire attacker-controlled source in one parser action.
	swiftMaximumExternalScanAdvances = 64 << 10
	// Bound nested comments independently of the byte budget so a future
	// scanner change cannot turn depth into unbounded arithmetic state.
	swiftMaximumCommentDepth = 256
	// The pinned scanner serializes this count as uint32. A tighter bound keeps
	// restored parser-owned state within the scanner's actual lookahead budget.
	swiftMaximumRawStringHashes = swiftMaximumExternalScanAdvances
)

type swiftScanner struct {
	ongoingRawStringHashCount uint32
}

var swiftLanguageOnce sync.Once
var swiftLanguage *treesitter.Language

// Language returns the immutable generated Swift language with a factory that
// creates independent external-scanner state for every parser stack.
func Language() *treesitter.Language {
	swiftLanguageOnce.Do(func() {
		swiftLanguage = SwiftLanguage()
		swiftLanguage.NewExternalScanner = newSwiftScanner
	})
	return swiftLanguage
}

func newSwiftScanner() treesitter.ExternalScanner {
	return &swiftScanner{}
}

func (scanner *swiftScanner) Serialize(buffer []byte) uint32 {
	if scanner == nil || len(buffer) < 4 ||
		scanner.ongoingRawStringHashCount > swiftMaximumRawStringHashes {
		return 0
	}
	binary.BigEndian.PutUint32(buffer[:4], scanner.ongoingRawStringHashCount)
	return 4
}

func (scanner *swiftScanner) Deserialize(data []byte) {
	if scanner == nil {
		return
	}
	scanner.ongoingRawStringHashCount = 0
	if len(data) != 4 {
		return
	}
	count := binary.BigEndian.Uint32(data)
	if count > swiftMaximumRawStringHashes {
		return
	}
	scanner.ongoingRawStringHashCount = count
}

type swiftScanCursor struct {
	lexer     *treesitter.Lexer
	remaining int
	exhausted bool
}

func newSwiftScanCursor(lexer *treesitter.Lexer) swiftScanCursor {
	return swiftScanCursor{
		lexer:     lexer,
		remaining: swiftMaximumExternalScanAdvances,
	}
}

func (cursor *swiftScanCursor) atEnd() bool {
	// The C scanner treats a NUL lookahead as EOF. Preserve that behavior for
	// byte-for-byte grammar parity while still recognizing the Go runtime's -1.
	return cursor == nil || cursor.lexer == nil || cursor.lexer.EOF() ||
		cursor.lexer.Lookahead == 0
}

func (cursor *swiftScanCursor) advance(skip bool) bool {
	if cursor == nil || cursor.lexer == nil || cursor.atEnd() {
		return false
	}
	if cursor.remaining == 0 {
		cursor.exhausted = true
		return false
	}
	cursor.remaining--
	cursor.lexer.Advance(skip)
	return true
}

func (cursor *swiftScanCursor) rejectLimit() {
	if cursor != nil {
		cursor.exhausted = true
	}
}

func swiftValidSymbol(validSymbols []bool, symbol int) bool {
	return symbol >= 0 && symbol < len(validSymbols) && validSymbols[symbol]
}

type swiftTerminatorGroup uint8

const (
	swiftTerminatorAlphaNumeric swiftTerminatorGroup = iota
	swiftTerminatorOperatorSymbols
	swiftTerminatorOperatorOrDot
	swiftTerminatorNonWhitespace
)

type swiftFixedOperator struct {
	text       string
	terminator swiftTerminatorGroup
	symbol     int
	suppressor uint64
}

const (
	swiftOperatorArrow = iota
	swiftOperatorDot
	swiftOperatorConjunction
	swiftOperatorDisjunction
	swiftOperatorNilCoalescing
	swiftOperatorEqual
	swiftOperatorEquality
	swiftOperatorPlusThenWhitespace
	swiftOperatorMinusThenWhitespace
	swiftOperatorBang
	swiftOperatorThrows
	swiftOperatorRethrows
	swiftOperatorDefault
	swiftOperatorWhere
	swiftOperatorElse
	swiftOperatorCatch
	swiftOperatorAs
	swiftOperatorAsQuestion
	swiftOperatorAsBang
	swiftOperatorAsync
)

var swiftFixedOperators = [...]swiftFixedOperator{
	{text: "->", terminator: swiftTerminatorOperatorSymbols, symbol: swiftArrowOperator},
	{text: ".", terminator: swiftTerminatorOperatorOrDot, symbol: swiftDotOperator},
	{text: "&&", terminator: swiftTerminatorOperatorSymbols, symbol: swiftConjunctionOperator},
	{text: "||", terminator: swiftTerminatorOperatorSymbols, symbol: swiftDisjunctionOperator},
	{text: "??", terminator: swiftTerminatorOperatorSymbols, symbol: swiftNilCoalescingOperator},
	{text: "=", terminator: swiftTerminatorOperatorSymbols, symbol: swiftEqualSign},
	{text: "==", terminator: swiftTerminatorOperatorSymbols, symbol: swiftEqualEqual},
	{text: "+", terminator: swiftTerminatorNonWhitespace, symbol: swiftPlusThenWhitespace},
	{text: "-", terminator: swiftTerminatorNonWhitespace, symbol: swiftMinusThenWhitespace},
	{
		text:       "!",
		terminator: swiftTerminatorOperatorSymbols,
		symbol:     swiftBang,
		suppressor: uint64(1) << swiftFakeTryBang,
	},
	{text: "throws", terminator: swiftTerminatorAlphaNumeric, symbol: swiftThrowsKeyword},
	{text: "rethrows", terminator: swiftTerminatorAlphaNumeric, symbol: swiftRethrowsKeyword},
	{text: "default", terminator: swiftTerminatorAlphaNumeric, symbol: swiftDefaultKeyword},
	{text: "where", terminator: swiftTerminatorAlphaNumeric, symbol: swiftWhereKeyword},
	{text: "else", terminator: swiftTerminatorAlphaNumeric, symbol: swiftElseKeyword},
	{text: "catch", terminator: swiftTerminatorAlphaNumeric, symbol: swiftCatchKeyword},
	{text: "as", terminator: swiftTerminatorAlphaNumeric, symbol: swiftAsKeyword},
	{text: "as?", terminator: swiftTerminatorOperatorSymbols, symbol: swiftAsQuestion},
	{text: "as!", terminator: swiftTerminatorOperatorSymbols, symbol: swiftAsBang},
	{text: "async", terminator: swiftTerminatorAlphaNumeric, symbol: swiftAsyncKeyword},
}

var swiftReservedOperators = [...]string{
	"/", "=", "-", "+", "!", "*", "%", "<", ">", "&", "|", "^", "?", "~",
	".", "..", "->", "/*", "*/", "+=", "-=", "*=", "/=", "%=", ">>", "<<",
	"++", "--", "===", "...", "..<",
}

func swiftIsCrossSemicolonToken(symbol int) bool {
	switch symbol {
	case swiftArrowOperator,
		swiftDotOperator,
		swiftConjunctionOperator,
		swiftDisjunctionOperator,
		swiftNilCoalescingOperator,
		swiftEqualSign,
		swiftEqualEqual,
		swiftPlusThenWhitespace,
		swiftMinusThenWhitespace,
		swiftThrowsKeyword,
		swiftRethrowsKeyword,
		swiftDefaultKeyword,
		swiftWhereKeyword,
		swiftElseKeyword,
		swiftCatchKeyword,
		swiftAsKeyword,
		swiftAsQuestion,
		swiftAsBang,
		swiftAsyncKeyword,
		swiftCustomOperator:
		return true
	default:
		return false
	}
}

func swiftIsWhitespace(character int32) bool {
	return character >= 0 && unicode.IsSpace(character)
}

func swiftIsIdentifierContinuation(character int32) bool {
	return character >= 0 && setContains(
		aux_sym_simple_identifier_token1_character_set_2,
		rune(character),
	)
}

func swiftIsBaseOperatorSymbol(character int32) bool {
	return character == '/' || character == '=' || character == '-' ||
		character == '+' || character == '!' || character == '*' ||
		character == '%' || character == '<' || character == '>' ||
		character == '&' || character == '|' || character == '^' ||
		character == '?' || character == '~'
}

func swiftIsLegalCustomOperator(index int, first, current int32) bool {
	firstCharacter := index == 0
	switch current {
	case '=', '-', '+', '!', '%', '<', '>', '&', '|', '^', '?', '~':
		return true
	case '.':
		return firstCharacter || first == '.'
	case '*', '/':
		return index != 1 || first != '/'
	default:
		if (current >= 0x00a1 && current <= 0x00a7) ||
			current == 0x00a9 || current == 0x00ab || current == 0x00ac ||
			current == 0x00ae || (current >= 0x00b0 && current <= 0x00b1) ||
			current == 0x00b6 || current == 0x00bb || current == 0x00bf ||
			current == 0x00d7 || current == 0x00f7 ||
			(current >= 0x2016 && current <= 0x2017) ||
			(current >= 0x2020 && current <= 0x2027) ||
			(current >= 0x2030 && current <= 0x203e) ||
			(current >= 0x2041 && current <= 0x2053) ||
			(current >= 0x2055 && current <= 0x205e) ||
			(current >= 0x2190 && current <= 0x23ff) ||
			(current >= 0x2500 && current <= 0x2775) ||
			(current >= 0x2794 && current <= 0x2bff) ||
			(current >= 0x2e00 && current <= 0x2e7f) ||
			(current >= 0x3001 && current <= 0x3003) ||
			(current >= 0x3008 && current <= 0x3020) || current == 0x3030 {
			return true
		}
		if (current >= 0x0300 && current <= 0x036f) ||
			(current >= 0x1dc0 && current <= 0x1dff) ||
			(current >= 0x20d0 && current <= 0x20ff) ||
			(current >= 0xfe00 && current <= 0xfe0f) ||
			(current >= 0xfe20 && current <= 0xfe2f) ||
			(current >= 0xe0100 && current <= 0xe01ef) {
			return !firstCharacter
		}
		return false
	}
}

func swiftCollectFixedOperatorCandidates(
	character int32,
	validSymbols []bool,
) []int {
	var candidates []int
	appendIfValid := func(operator int) {
		if swiftValidSymbol(validSymbols, swiftFixedOperators[operator].symbol) {
			candidates = append(candidates, operator)
		}
	}
	switch character {
	case '-':
		appendIfValid(swiftOperatorArrow)
		appendIfValid(swiftOperatorMinusThenWhitespace)
	case '.':
		appendIfValid(swiftOperatorDot)
	case '&':
		appendIfValid(swiftOperatorConjunction)
	case '|':
		appendIfValid(swiftOperatorDisjunction)
	case '?':
		appendIfValid(swiftOperatorNilCoalescing)
	case '=':
		appendIfValid(swiftOperatorEqual)
		appendIfValid(swiftOperatorEquality)
	case '+':
		appendIfValid(swiftOperatorPlusThenWhitespace)
	case '!':
		appendIfValid(swiftOperatorBang)
	case 't':
		appendIfValid(swiftOperatorThrows)
	case 'r':
		appendIfValid(swiftOperatorRethrows)
	case 'd':
		appendIfValid(swiftOperatorDefault)
	case 'w':
		appendIfValid(swiftOperatorWhere)
	case 'e':
		appendIfValid(swiftOperatorElse)
	case 'c':
		appendIfValid(swiftOperatorCatch)
	case 'a':
		appendIfValid(swiftOperatorAs)
		appendIfValid(swiftOperatorAsQuestion)
		appendIfValid(swiftOperatorAsBang)
		appendIfValid(swiftOperatorAsync)
	}
	return candidates
}

func swiftFixedOperatorCanEnd(operator swiftFixedOperator, lookahead int32) bool {
	switch operator.terminator {
	case swiftTerminatorOperatorSymbols:
		return !swiftIsBaseOperatorSymbol(lookahead)
	case swiftTerminatorOperatorOrDot:
		return !swiftIsBaseOperatorSymbol(lookahead) && lookahead != '.'
	case swiftTerminatorAlphaNumeric:
		return !swiftIsIdentifierContinuation(lookahead)
	case swiftTerminatorNonWhitespace:
		return swiftIsWhitespace(lookahead)
	default:
		return false
	}
}

func swiftAnyReservedOperator(states []uint8) bool {
	for _, state := range states {
		if state == 2 {
			return true
		}
	}
	return false
}

func swiftEatOperators(
	cursor *swiftScanCursor,
	validSymbols []bool,
	markEnd bool,
	priorCharacter int32,
) (bool, int) {
	if cursor == nil || cursor.atEnd() {
		return false, 0
	}
	firstCharacter := cursor.lexer.Lookahead
	stringIndex := 0
	if priorCharacter != 0 {
		firstCharacter = priorCharacter
		stringIndex = 1
	}
	possibleCustom := swiftValidSymbol(validSymbols, swiftCustomOperator) &&
		swiftIsLegalCustomOperator(0, firstCharacter, firstCharacter)
	candidates := swiftCollectFixedOperatorCandidates(firstCharacter, validSymbols)
	if len(candidates) == 0 && !possibleCustom {
		return false, 0
	}

	reservedStates := make([]uint8, len(swiftReservedOperators))
	if possibleCustom {
		for index, reserved := range swiftReservedOperators {
			if int32(reserved[0]) == firstCharacter {
				reservedStates[index] = 1
			}
		}
	}
	lastExaminedCharacter := firstCharacter
	fullMatch := -1

	for {
		for candidateIndex := 0; candidateIndex < len(candidates); {
			operatorIndex := candidates[candidateIndex]
			operator := swiftFixedOperators[operatorIndex]
			if stringIndex == len(operator.text) {
				if swiftFixedOperatorCanEnd(operator, cursor.lexer.Lookahead) {
					fullMatch = operatorIndex
					if markEnd {
						cursor.lexer.MarkEnd()
					}
				}
				candidates[candidateIndex] = candidates[len(candidates)-1]
				candidates = candidates[:len(candidates)-1]
				continue
			}
			if stringIndex > len(operator.text) ||
				int32(operator.text[stringIndex]) != cursor.lexer.Lookahead {
				candidates[candidateIndex] = candidates[len(candidates)-1]
				candidates = candidates[:len(candidates)-1]
				continue
			}
			candidateIndex++
		}

		if possibleCustom {
			for index, reserved := range swiftReservedOperators {
				if reservedStates[index] == 0 {
					continue
				}
				if stringIndex >= len(reserved) {
					reservedStates[index] = 0
					continue
				}
				if int32(reserved[stringIndex]) != cursor.lexer.Lookahead {
					reservedStates[index] = 0
					continue
				}
				if stringIndex+1 == len(reserved) {
					reservedStates[index] = 2
				}
			}
		}

		possibleCustom = possibleCustom && swiftIsLegalCustomOperator(
			stringIndex,
			firstCharacter,
			cursor.lexer.Lookahead,
		)
		fixedCandidates := len(candidates)
		if fixedCandidates == 0 {
			if !possibleCustom {
				break
			}
			if markEnd && fullMatch == -1 {
				cursor.lexer.MarkEnd()
			}
		}

		lastExaminedCharacter = cursor.lexer.Lookahead
		if !cursor.advance(false) {
			if cursor.exhausted {
				return false, 0
			}
			break
		}
		stringIndex++
		if fixedCandidates == 0 && !swiftIsLegalCustomOperator(
			stringIndex,
			firstCharacter,
			cursor.lexer.Lookahead,
		) {
			break
		}
	}

	if cursor.exhausted {
		return false, 0
	}
	if fullMatch >= 0 {
		operator := swiftFixedOperators[fullMatch]
		if operator.suppressor != 0 {
			for symbol := range swiftExternalTokenCount {
				if operator.suppressor&(uint64(1)<<symbol) != 0 &&
					swiftValidSymbol(validSymbols, symbol) {
					return false, 0
				}
			}
		}
		return true, operator.symbol
	}
	if possibleCustom && !swiftAnyReservedOperator(reservedStates) {
		if (lastExaminedCharacter != '<' ||
			swiftIsWhitespace(cursor.lexer.Lookahead)) && markEnd {
			cursor.lexer.MarkEnd()
		}
		return true, swiftCustomOperator
	}
	return false, 0
}

type swiftParseDirective uint8

const (
	swiftContinueNothing swiftParseDirective = iota
	swiftContinueToken
	swiftContinueSlashConsumed
	swiftStopNothing
	swiftStopToken
	swiftStopEndOfFile
)

func swiftEatComment(cursor *swiftScanCursor, markEnd bool) swiftParseDirective {
	if cursor == nil || cursor.atEnd() || cursor.lexer.Lookahead != '/' {
		return swiftContinueNothing
	}
	if !cursor.advance(false) {
		return swiftStopEndOfFile
	}
	if cursor.atEnd() || cursor.lexer.Lookahead != '*' {
		return swiftContinueSlashConsumed
	}
	if !cursor.advance(false) {
		return swiftStopEndOfFile
	}

	afterStar := false
	depth := 1
	for !cursor.atEnd() {
		switch cursor.lexer.Lookahead {
		case '*':
			if !cursor.advance(false) {
				return swiftStopEndOfFile
			}
			afterStar = true
		case '/':
			if afterStar {
				if !cursor.advance(false) && cursor.exhausted {
					return swiftStopNothing
				}
				afterStar = false
				depth--
				if depth == 0 {
					if markEnd {
						cursor.lexer.MarkEnd()
					}
					return swiftStopToken
				}
				continue
			}
			if !cursor.advance(false) {
				return swiftStopEndOfFile
			}
			afterStar = false
			if !cursor.atEnd() && cursor.lexer.Lookahead == '*' {
				if depth == swiftMaximumCommentDepth {
					cursor.rejectLimit()
					return swiftStopNothing
				}
				depth++
				if !cursor.advance(false) {
					return swiftStopEndOfFile
				}
			}
		default:
			if !cursor.advance(false) {
				return swiftStopEndOfFile
			}
			afterStar = false
		}
		if cursor.exhausted {
			return swiftStopNothing
		}
	}
	return swiftStopEndOfFile
}

func swiftEatWhitespace(
	cursor *swiftScanCursor,
	validSymbols []bool,
) (swiftParseDirective, int) {
	directive := swiftContinueNothing
	semicolonValid := swiftValidSymbol(validSymbols, swiftImplicitSemicolon) &&
		swiftValidSymbol(validSymbols, swiftExplicitSemicolon)
	lookahead := int32(-1)
	for !cursor.atEnd() {
		lookahead = cursor.lexer.Lookahead
		if !swiftIsWhitespace(lookahead) && lookahead != ';' {
			break
		}
		if lookahead == ';' {
			if semicolonValid {
				directive = swiftStopToken
				if !cursor.advance(false) && cursor.exhausted {
					return swiftStopNothing, 0
				}
			}
			break
		}
		if !cursor.advance(true) {
			return swiftStopNothing, 0
		}
		cursor.lexer.MarkEnd()
		if directive == swiftContinueNothing && (lookahead == '\n' || lookahead == '\r') {
			directive = swiftContinueToken
		}
	}
	if cursor.exhausted {
		return swiftStopNothing, 0
	}

	if directive == swiftContinueToken && !cursor.atEnd() &&
		cursor.lexer.Lookahead == '/' {
		for !cursor.atEnd() && cursor.lexer.Lookahead == '/' {
			commentDirective := swiftEatComment(cursor, false)
			switch commentDirective {
			case swiftStopToken:
				cursor.lexer.MarkEnd()
				return swiftStopToken, swiftBlockComment
			case swiftStopEndOfFile:
				return swiftStopEndOfFile, 0
			case swiftStopNothing:
				return swiftStopNothing, 0
			case swiftContinueSlashConsumed:
				return swiftContinueSlashConsumed, 0
			case swiftContinueNothing, swiftContinueToken:
				if !cursor.atEnd() && swiftIsWhitespace(cursor.lexer.Lookahead) {
					return swiftStopNothing, 0
				}
			}
			for !cursor.atEnd() && swiftIsWhitespace(cursor.lexer.Lookahead) {
				if !cursor.advance(true) {
					return swiftStopNothing, 0
				}
			}
		}

		if sawOperator, _ := swiftEatOperators(
			cursor,
			validSymbols,
			false,
			0,
		); sawOperator {
			return swiftStopNothing, 0
		}
		directive = swiftStopToken
	}

	if directive == swiftContinueToken &&
		(lookahead == '?' || lookahead == ':' || lookahead == '{') {
		return swiftContinueNothing, 0
	}
	if semicolonValid && directive != swiftContinueNothing {
		if lookahead == ';' {
			return directive, swiftExplicitSemicolon
		}
		return directive, swiftImplicitSemicolon
	}
	return swiftContinueNothing, 0
}

var swiftCompilerDirectives = [...]struct {
	text   string
	symbol int
}{
	{text: "if", symbol: swiftDirectiveIf},
	{text: "elseif", symbol: swiftDirectiveElseIf},
	{text: "else", symbol: swiftDirectiveElse},
	{text: "endif", symbol: swiftDirectiveEndIf},
}

func swiftFindCompilerDirective(cursor *swiftScanCursor) int {
	possible := make([]bool, len(swiftCompilerDirectives))
	for index := range possible {
		possible[index] = true
	}
	stringIndex := 0
	fullMatch := -1
	for {
		matches := 0
		for index, directive := range swiftCompilerDirectives {
			if !possible[index] {
				continue
			}
			if stringIndex == len(directive.text) {
				fullMatch = index
				cursor.lexer.MarkEnd()
			}
			if stringIndex >= len(directive.text) ||
				int32(directive.text[stringIndex]) != cursor.lexer.Lookahead {
				possible[index] = false
				continue
			}
			matches++
		}
		if matches == 0 {
			break
		}
		if !cursor.advance(false) {
			if cursor.exhausted {
				return swiftHashSymbol
			}
			break
		}
		stringIndex++
	}
	if fullMatch < 0 {
		return swiftHashSymbol
	}
	return swiftCompilerDirectives[fullMatch].symbol
}

func (scanner *swiftScanner) eatRawStringPart(
	cursor *swiftScanCursor,
	validSymbols []bool,
) (bool, int) {
	if !swiftValidSymbol(validSymbols, swiftRawStringPart) {
		return false, 0
	}
	hashCount := scanner.ongoingRawStringHashCount
	if hashCount == 0 {
		for !cursor.atEnd() && cursor.lexer.Lookahead == '#' {
			if hashCount == swiftMaximumRawStringHashes {
				cursor.rejectLimit()
				return false, 0
			}
			hashCount++
			if !cursor.advance(false) {
				return false, 0
			}
		}
		if hashCount == 0 {
			return false, 0
		}
		switch {
		case !cursor.atEnd() && cursor.lexer.Lookahead == '"':
			if !cursor.advance(false) {
				return false, 0
			}
		case hashCount == 1:
			cursor.lexer.MarkEnd()
			return true, swiftFindCompilerDirective(cursor)
		default:
			return false, 0
		}
	} else if !swiftValidSymbol(validSymbols, swiftRawStringContinuingIndicator) {
		return false, 0
	}

	for !cursor.atEnd() {
		lastCharacter := int32(0)
		cursor.lexer.MarkEnd()
		for !cursor.atEnd() && cursor.lexer.Lookahead != '#' {
			lastCharacter = cursor.lexer.Lookahead
			if !cursor.advance(false) {
				return false, 0
			}
			if lastCharacter != '\\' ||
				(!cursor.atEnd() && cursor.lexer.Lookahead == '\\') {
				cursor.lexer.MarkEnd()
			}
		}

		currentHashCount := uint32(0)
		for !cursor.atEnd() && cursor.lexer.Lookahead == '#' &&
			currentHashCount < hashCount {
			currentHashCount++
			if !cursor.advance(false) {
				return false, 0
			}
		}
		if currentHashCount == hashCount {
			if lastCharacter == '\\' && !cursor.atEnd() &&
				cursor.lexer.Lookahead == '(' {
				scanner.ongoingRawStringHashCount = hashCount
				return true, swiftRawStringPart
			}
			if lastCharacter == '"' {
				cursor.lexer.MarkEnd()
				scanner.ongoingRawStringHashCount = 0
				return true, swiftRawStringEndPart
			}
		}
	}
	return false, 0
}

func (scanner *swiftScanner) Scan(
	lexer *treesitter.Lexer,
	validSymbols []bool,
) bool {
	if scanner == nil || lexer == nil || len(validSymbols) < swiftExternalTokenCount {
		return false
	}
	cursor := newSwiftScanCursor(lexer)
	whitespaceDirective, whitespaceSymbol := swiftEatWhitespace(&cursor, validSymbols)
	if cursor.exhausted {
		return false
	}
	if whitespaceDirective == swiftStopToken {
		lexer.ResultSymbol = treesitter.Symbol(whitespaceSymbol)
		return true
	}
	if whitespaceDirective == swiftStopNothing ||
		whitespaceDirective == swiftStopEndOfFile {
		return false
	}
	hasWhitespaceResult := whitespaceDirective == swiftContinueToken

	commentDirective := whitespaceDirective
	if whitespaceDirective != swiftContinueSlashConsumed {
		commentDirective = swiftEatComment(&cursor, true)
	}
	if cursor.exhausted {
		return false
	}
	if commentDirective == swiftStopToken {
		lexer.MarkEnd()
		lexer.ResultSymbol = treesitter.Symbol(swiftBlockComment)
		return true
	}
	if commentDirective == swiftStopEndOfFile {
		return false
	}

	priorCharacter := int32(0)
	if commentDirective == swiftContinueSlashConsumed {
		priorCharacter = '/'
	}
	sawOperator, operatorSymbol := swiftEatOperators(
		&cursor,
		validSymbols,
		!hasWhitespaceResult,
		priorCharacter,
	)
	if cursor.exhausted {
		return false
	}
	if sawOperator && (!hasWhitespaceResult ||
		swiftIsCrossSemicolonToken(operatorSymbol)) {
		lexer.ResultSymbol = treesitter.Symbol(operatorSymbol)
		if hasWhitespaceResult {
			lexer.MarkEnd()
		}
		return true
	}
	if hasWhitespaceResult {
		lexer.ResultSymbol = treesitter.Symbol(whitespaceSymbol)
		return true
	}

	matched, rawStringSymbol := scanner.eatRawStringPart(&cursor, validSymbols)
	if cursor.exhausted {
		return false
	}
	if matched {
		lexer.ResultSymbol = treesitter.Symbol(rawStringSymbol)
		return true
	}
	return false
}
