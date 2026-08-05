// Package kotlingrammar provides the generated Kotlin grammar and its
// external scanner for the pure-Go Tree-sitter runtime.
package kotlingrammar

import (
	"sync"
	"unicode"

	treesitter "github.com/dcosson/treesitter-go"
)

// External token indexes must remain in the same order as grammar.js.
const (
	kotlinAutomaticSemicolon = iota
	kotlinMultilineComment
	kotlinStringStart
	kotlinStringEnd
	kotlinStringContent
	kotlinPrimaryConstructorKeyword
	kotlinImportDot
	kotlinInterpolationExpressionStart
	kotlinInterpolationIdentifierStart
	kotlinByDelegationHint
	kotlinExternalTokenCount
)

const (
	// String contents, comments, whitespace, and ambiguity probes can otherwise
	// scan an entire attacker-controlled source in one parser action.
	kotlinMaximumExternalScanAdvances = 64 << 10
	// Each serialized string frame occupies two bytes. This cap keeps scanner
	// state well below the runtime's 1024-byte serialization buffer and bounds
	// parser-owned scanner-state copies.
	kotlinMaximumStringDepth = 64
	// Bound nested comments, annotation arguments, and annotation chains used by
	// automatic-semicolon and constructor lookahead.
	kotlinMaximumProbeDepth = 128
	// The pinned scanner stores the multi-dollar interpolation prefix in a byte.
	kotlinMaximumInterpolationPrefix = 255
)

const (
	kotlinRegularStringDelimiter = byte('"')
	kotlinTripleStringDelimiter  = kotlinRegularStringDelimiter + 1
)

type kotlinStringFrame struct {
	triple       bool
	prefixLength uint8
}

type kotlinScanner struct {
	strings []kotlinStringFrame
}

var kotlinLanguageOnce sync.Once
var kotlinLanguage *treesitter.Language

// Language returns the generated Kotlin language with a fresh external
// scanner factory. The returned parse tables are immutable after assignment.
func Language() *treesitter.Language {
	kotlinLanguageOnce.Do(func() {
		kotlinLanguage = KotlinLanguage()
		kotlinLanguage.NewExternalScanner = newKotlinScanner
	})
	return kotlinLanguage
}

func newKotlinScanner() treesitter.ExternalScanner {
	return &kotlinScanner{}
}

// Serialize uses the pinned scanner's two-byte frame format:
// [delimiter byte, interpolation-prefix byte].
func (scanner *kotlinScanner) Serialize(buffer []byte) uint32 {
	if scanner == nil || len(scanner.strings) > kotlinMaximumStringDepth {
		return 0
	}
	required := len(scanner.strings) * 2
	if required > len(buffer) {
		return 0
	}
	for index, frame := range scanner.strings {
		if frame.prefixLength == 0 {
			return 0
		}
		delimiter := kotlinRegularStringDelimiter
		if frame.triple {
			delimiter = kotlinTripleStringDelimiter
		}
		buffer[index*2] = delimiter
		buffer[index*2+1] = frame.prefixLength
	}
	return uint32(required)
}

// Deserialize rejects truncated, oversized, or semantically invalid state
// instead of retaining attacker-controlled scanner frames.
func (scanner *kotlinScanner) Deserialize(data []byte) {
	if scanner == nil {
		return
	}
	scanner.strings = scanner.strings[:0]
	if len(data) == 0 {
		return
	}
	if len(data)%2 != 0 || len(data)/2 > kotlinMaximumStringDepth {
		return
	}
	count := len(data) / 2
	if cap(scanner.strings) < count {
		scanner.strings = make([]kotlinStringFrame, 0, count)
	}
	for offset := 0; offset < len(data); offset += 2 {
		delimiter, prefix := data[offset], data[offset+1]
		if (delimiter != kotlinRegularStringDelimiter &&
			delimiter != kotlinTripleStringDelimiter) || prefix == 0 {
			scanner.strings = scanner.strings[:0]
			return
		}
		scanner.strings = append(scanner.strings, kotlinStringFrame{
			triple:       delimiter == kotlinTripleStringDelimiter,
			prefixLength: prefix,
		})
	}
}

type kotlinScanCursor struct {
	lexer     *treesitter.Lexer
	remaining int
	limitHit  bool
}

func newKotlinScanCursor(lexer *treesitter.Lexer) kotlinScanCursor {
	return kotlinScanCursor{
		lexer:     lexer,
		remaining: kotlinMaximumExternalScanAdvances,
	}
}

func (cursor *kotlinScanCursor) advance(skip bool) bool {
	if cursor == nil || cursor.lexer == nil {
		if cursor != nil {
			cursor.limitHit = true
		}
		return false
	}
	if cursor.lexer.EOF() {
		return false
	}
	if cursor.remaining == 0 {
		cursor.limitHit = true
		return false
	}
	cursor.remaining--
	cursor.lexer.Advance(skip)
	return true
}

func (cursor *kotlinScanCursor) rejectLimit() bool {
	if cursor != nil {
		cursor.limitHit = true
	}
	return false
}

func (cursor *kotlinScanCursor) skipWhitespace() bool {
	for cursor != nil && cursor.lexer != nil && !cursor.lexer.EOF() &&
		unicode.IsSpace(cursor.lexer.Lookahead) {
		if !cursor.advance(true) {
			return false
		}
	}
	return cursor != nil && cursor.lexer != nil
}

func (scanner *kotlinScanner) Scan(
	lexer *treesitter.Lexer,
	validSymbols []bool,
) bool {
	if scanner == nil || lexer == nil || len(validSymbols) < kotlinExternalTokenCount {
		return false
	}
	cursor := newKotlinScanCursor(lexer)

	// BY_DELEGATION_HINT is a parser-context flag only. The scanner never emits
	// it, but automatic-semicolon insertion consults its validity.
	if validSymbols[kotlinAutomaticSemicolon] {
		if scanner.scanAutomaticSemicolon(&cursor, validSymbols) {
			return true
		}
		if cursor.limitHit {
			return false
		}
	}
	if validSymbols[kotlinImportDot] && scanner.scanImportDot(&cursor) {
		return true
	}
	if cursor.limitHit {
		return false
	}

	if validSymbols[kotlinPrimaryConstructorKeyword] &&
		!validSymbols[kotlinStringContent] {
		if !cursor.skipWhitespace() {
			return false
		}
		if lexer.Lookahead == 'c' && kotlinConsumeToken(&cursor, "constructor") {
			lexer.ResultSymbol = treesitter.Symbol(kotlinPrimaryConstructorKeyword)
			lexer.MarkEnd()
			return true
		}
		if cursor.limitHit {
			return false
		}
	}

	if validSymbols[kotlinStringContent] ||
		validSymbols[kotlinInterpolationExpressionStart] ||
		validSymbols[kotlinInterpolationIdentifierStart] {
		if scanner.scanStringContent(&cursor, validSymbols) {
			return true
		}
		if cursor.limitHit {
			return false
		}
	}

	if !cursor.skipWhitespace() {
		return false
	}
	if validSymbols[kotlinStringStart] && scanner.scanStringStart(&cursor) {
		lexer.ResultSymbol = treesitter.Symbol(kotlinStringStart)
		return true
	}
	if cursor.limitHit {
		return false
	}
	return validSymbols[kotlinMultilineComment] && scanner.scanMultilineComment(&cursor)
}

func (scanner *kotlinScanner) scanStringStart(cursor *kotlinScanCursor) bool {
	if len(scanner.strings) >= kotlinMaximumStringDepth {
		return false
	}
	prefixLength := 0
	for !cursor.lexer.EOF() && cursor.lexer.Lookahead == '$' {
		if !cursor.advance(false) {
			return false
		}
		if prefixLength < kotlinMaximumInterpolationPrefix {
			prefixLength++
		}
	}
	if prefixLength == 0 {
		prefixLength = 1
	}
	if cursor.lexer.EOF() || cursor.lexer.Lookahead != '"' ||
		!cursor.advance(false) {
		return false
	}
	cursor.lexer.MarkEnd()
	for count := 1; count < 3; count++ {
		if cursor.lexer.Lookahead != '"' {
			scanner.strings = append(scanner.strings, kotlinStringFrame{
				prefixLength: uint8(prefixLength),
			})
			return true
		}
		if !cursor.advance(false) {
			return false
		}
	}
	cursor.lexer.MarkEnd()
	scanner.strings = append(scanner.strings, kotlinStringFrame{
		triple:       true,
		prefixLength: uint8(prefixLength),
	})
	return true
}

func (scanner *kotlinScanner) scanStringContent(
	cursor *kotlinScanCursor,
	validSymbols []bool,
) bool {
	if len(scanner.strings) == 0 {
		return false
	}
	frame := scanner.strings[len(scanner.strings)-1]
	endCharacter := int32('"')
	hasContent := false

	for !cursor.lexer.EOF() {
		if cursor.lexer.Lookahead == 0 {
			// In the Go runtime EOF is -1. A literal NUL is ordinary string
			// content and must be consumed to guarantee forward progress.
			if !cursor.advance(false) {
				return false
			}
			hasContent = true
			continue
		}
		if cursor.lexer.Lookahead == '$' {
			if hasContent {
				cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
				return true
			}
			if !cursor.advance(false) {
				return false
			}
			cursor.lexer.MarkEnd()
			additionalDollars := 0
			for !cursor.lexer.EOF() && cursor.lexer.Lookahead == '$' {
				if !cursor.advance(false) {
					return false
				}
				additionalDollars++
			}
			totalDollars := 1 + additionalDollars
			canInterpolate := kotlinIsLetter(cursor.lexer.Lookahead) ||
				cursor.lexer.Lookahead == '_' || cursor.lexer.Lookahead == '{'
			if totalDollars >= int(frame.prefixLength) && canInterpolate {
				if totalDollars > int(frame.prefixLength) {
					cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
					return true
				}
				if additionalDollars > 0 {
					cursor.lexer.MarkEnd()
				}
				if validSymbols[kotlinInterpolationExpressionStart] &&
					cursor.lexer.Lookahead == '{' {
					if !cursor.advance(false) {
						return false
					}
					if cursor.lexer.Lookahead == '}' {
						return false
					}
					cursor.lexer.MarkEnd()
					cursor.lexer.ResultSymbol = treesitter.Symbol(
						kotlinInterpolationExpressionStart,
					)
					return true
				}
				if validSymbols[kotlinInterpolationIdentifierStart] &&
					(kotlinIsLetter(cursor.lexer.Lookahead) ||
						cursor.lexer.Lookahead == '_') {
					cursor.lexer.ResultSymbol = treesitter.Symbol(
						kotlinInterpolationIdentifierStart,
					)
					return true
				}
				return false
			}
			if additionalDollars > 0 {
				cursor.lexer.MarkEnd()
			}
			cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
			return true
		}

		if cursor.lexer.Lookahead == '\\' {
			if !cursor.advance(false) {
				return false
			}
			if cursor.lexer.Lookahead == '$' {
				if !cursor.advance(false) {
					return false
				}
				if cursor.lexer.Lookahead == endCharacter {
					if !cursor.advance(false) {
						return false
					}
					scanner.popString()
					cursor.lexer.MarkEnd()
					cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringEnd)
					return true
				}
			} else if frame.triple && cursor.lexer.Lookahead == endCharacter {
				hasContent = true
				continue
			}
		} else if cursor.lexer.Lookahead == endCharacter {
			if frame.triple {
				cursor.lexer.MarkEnd()
				for count := 1; count < 3; count++ {
					if !cursor.advance(false) {
						return false
					}
					if cursor.lexer.Lookahead != endCharacter {
						cursor.lexer.MarkEnd()
						cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
						return true
					}
				}
				if hasContent && cursor.lexer.Lookahead == endCharacter {
					cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
					return true
				}
				cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringEnd)
				cursor.lexer.MarkEnd()
				for cursor.lexer.Lookahead == endCharacter {
					if !cursor.advance(false) {
						return false
					}
					cursor.lexer.MarkEnd()
				}
				scanner.popString()
				return true
			}
			if hasContent {
				cursor.lexer.MarkEnd()
				cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringContent)
				return true
			}
			if !cursor.advance(false) {
				return false
			}
			scanner.popString()
			cursor.lexer.MarkEnd()
			cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinStringEnd)
			return true
		}

		if cursor.lexer.EOF() || !cursor.advance(false) {
			return false
		}
		hasContent = true
	}
	return false
}

func (scanner *kotlinScanner) popString() {
	if scanner != nil && len(scanner.strings) > 0 {
		scanner.strings = scanner.strings[:len(scanner.strings)-1]
	}
}

func (scanner *kotlinScanner) scanMultilineComment(cursor *kotlinScanCursor) bool {
	if cursor.lexer.EOF() || cursor.lexer.Lookahead != '/' ||
		!cursor.advance(false) || cursor.lexer.Lookahead != '*' ||
		!cursor.advance(false) {
		return false
	}
	afterStar := false
	depth := 1
	for {
		if cursor.lexer.EOF() {
			cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
			cursor.lexer.MarkEnd()
			return true
		}
		switch cursor.lexer.Lookahead {
		case '*':
			if !cursor.advance(false) {
				return false
			}
			afterStar = true
		case '/':
			if !cursor.advance(false) {
				return false
			}
			if afterStar {
				afterStar = false
				depth--
				if depth == 0 {
					cursor.lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
					cursor.lexer.MarkEnd()
					return true
				}
			} else {
				afterStar = false
				if cursor.lexer.Lookahead == '*' {
					if depth == kotlinMaximumProbeDepth {
						return cursor.rejectLimit()
					}
					depth++
					if !cursor.advance(false) {
						return false
					}
				}
			}
		default:
			// This includes a literal NUL. EOF is represented by -1 in the Go
			// runtime, so consuming NUL is required for forward progress.
			if !cursor.advance(false) {
				return false
			}
			afterStar = false
		}
	}
}

func kotlinIsLetter(character int32) bool {
	return character >= 0 && unicode.IsLetter(character)
}

func kotlinIsWordCharacter(character int32) bool {
	return character == '_' || kotlinIsLetter(character) ||
		(character >= 0 && unicode.IsDigit(character))
}

// kotlinScanForWord consumes the already-inspected first character, the
// supplied remainder, and verifies an identifier boundary.
func kotlinScanForWord(cursor *kotlinScanCursor, remainder string) bool {
	if !cursor.advance(true) {
		return false
	}
	for _, character := range remainder {
		if cursor.lexer.Lookahead != character || !cursor.advance(true) {
			return false
		}
	}
	return !kotlinIsWordCharacter(cursor.lexer.Lookahead)
}

func kotlinCheckWord(cursor *kotlinScanCursor, word string) bool {
	for _, character := range word {
		if cursor.lexer.Lookahead != character || !cursor.advance(true) {
			return false
		}
	}
	return !kotlinIsWordCharacter(cursor.lexer.Lookahead)
}

func kotlinConsumeToken(cursor *kotlinScanCursor, word string) bool {
	for _, character := range word {
		if cursor.lexer.Lookahead != character || !cursor.advance(false) {
			return false
		}
	}
	return !kotlinIsWordCharacter(cursor.lexer.Lookahead)
}

func kotlinSkipWhitespaceAndComments(cursor *kotlinScanCursor) bool {
	for {
		if !cursor.skipWhitespace() {
			return false
		}
		if cursor.lexer.EOF() || cursor.lexer.Lookahead != '/' {
			return true
		}
		if !cursor.advance(true) {
			return false
		}
		switch cursor.lexer.Lookahead {
		case '/':
			if !cursor.advance(true) {
				return false
			}
			for !cursor.lexer.EOF() && cursor.lexer.Lookahead != '\n' &&
				cursor.lexer.Lookahead != '\r' {
				if !cursor.advance(true) {
					return false
				}
			}
		case '*':
			if !cursor.advance(true) {
				return false
			}
			depth := 1
			for depth > 0 && !cursor.lexer.EOF() {
				switch cursor.lexer.Lookahead {
				case '*':
					if !cursor.advance(true) {
						return false
					}
					if cursor.lexer.Lookahead == '/' {
						if !cursor.advance(true) {
							return false
						}
						depth--
					}
				case '/':
					if !cursor.advance(true) {
						return false
					}
					if cursor.lexer.Lookahead == '*' {
						if depth == kotlinMaximumProbeDepth {
							return cursor.rejectLimit()
						}
						depth++
						if !cursor.advance(true) {
							return false
						}
					}
				default:
					if !cursor.advance(true) {
						return false
					}
				}
			}
		default:
			return false
		}
	}
}

func kotlinFollowedByArrow(cursor *kotlinScanCursor) bool {
	if !kotlinSkipWhitespaceAndComments(cursor) ||
		cursor.lexer.Lookahead != '-' || !cursor.advance(true) {
		return false
	}
	return cursor.lexer.Lookahead == '>'
}

func kotlinCheckModifierThenConstructor(cursor *kotlinScanCursor) bool {
	var word [19]rune
	length := 0
	for kotlinIsWordCharacter(cursor.lexer.Lookahead) && length < len(word) {
		word[length] = cursor.lexer.Lookahead
		length++
		if !cursor.advance(true) {
			return false
		}
	}
	modifier := string(word[:length])
	if modifier != "public" && modifier != "private" &&
		modifier != "protected" && modifier != "internal" {
		return false
	}
	for cursor.lexer.Lookahead == ' ' || cursor.lexer.Lookahead == '\t' {
		if !cursor.advance(true) {
			return false
		}
	}
	return kotlinCheckWord(cursor, "constructor")
}

func kotlinCheckAnnotationThenConstructor(cursor *kotlinScanCursor) bool {
	annotationCount := 0
	for cursor.lexer.Lookahead == '@' {
		annotationCount++
		if annotationCount > kotlinMaximumProbeDepth {
			return cursor.rejectLimit()
		}
		if !cursor.advance(true) || !kotlinIsWordCharacter(cursor.lexer.Lookahead) {
			return false
		}
		for kotlinIsWordCharacter(cursor.lexer.Lookahead) {
			if !cursor.advance(true) {
				return false
			}
		}
		for cursor.lexer.Lookahead == '.' {
			if !cursor.advance(true) {
				return false
			}
			if !kotlinIsWordCharacter(cursor.lexer.Lookahead) {
				break
			}
			for kotlinIsWordCharacter(cursor.lexer.Lookahead) {
				if !cursor.advance(true) {
					return false
				}
			}
		}
		if cursor.lexer.Lookahead == '(' {
			depth := 1
			if !cursor.advance(true) {
				return false
			}
			for depth > 0 && cursor.lexer.Lookahead != 0 && !cursor.lexer.EOF() {
				if cursor.lexer.Lookahead == '"' {
					if !cursor.advance(true) {
						return false
					}
					for cursor.lexer.Lookahead != '"' && cursor.lexer.Lookahead != 0 &&
						!cursor.lexer.EOF() {
						if cursor.lexer.Lookahead == '\\' && !cursor.advance(true) {
							return false
						}
						if !cursor.advance(true) {
							return false
						}
					}
					if cursor.lexer.Lookahead == '"' && !cursor.advance(true) {
						return false
					}
					continue
				}
				switch cursor.lexer.Lookahead {
				case '(':
					if depth == kotlinMaximumProbeDepth {
						return cursor.rejectLimit()
					}
					depth++
				case ')':
					depth--
				}
				if !cursor.advance(true) {
					return false
				}
			}
			if depth != 0 {
				return false
			}
		}
		if !cursor.skipWhitespace() {
			return false
		}
	}
	if kotlinIsWordCharacter(cursor.lexer.Lookahead) &&
		cursor.lexer.Lookahead != 'c' {
		return kotlinCheckModifierThenConstructor(cursor)
	}
	return kotlinCheckWord(cursor, "constructor")
}

func (scanner *kotlinScanner) scanAutomaticSemicolon(
	cursor *kotlinScanCursor,
	validSymbols []bool,
) bool {
	lexer := cursor.lexer
	lexer.ResultSymbol = treesitter.Symbol(kotlinAutomaticSemicolon)
	lexer.MarkEnd()
	sameLine := true
	for {
		if lexer.EOF() {
			return true
		}
		if lexer.Lookahead == ';' {
			if !cursor.advance(false) {
				return false
			}
			lexer.MarkEnd()
			return true
		}
		if !unicode.IsSpace(lexer.Lookahead) {
			break
		}
		if lexer.Lookahead == '\n' {
			if !cursor.advance(true) {
				return false
			}
			sameLine = false
			break
		}
		if lexer.Lookahead == '\r' {
			if !cursor.advance(true) {
				return false
			}
			if lexer.Lookahead == '\n' && !cursor.advance(true) {
				return false
			}
			sameLine = false
			break
		}
		if !cursor.advance(true) {
			return false
		}
	}
	if !cursor.skipWhitespace() {
		return false
	}
	if sameLine {
		switch lexer.Lookahead {
		case 'i':
			return kotlinScanForWord(cursor, "mport")
		case ';':
			if !cursor.advance(false) {
				return false
			}
			lexer.MarkEnd()
			return true
		default:
			return false
		}
	}

	switch lexer.Lookahead {
	case ',', '.', ':', '*', '%', '>', '<', '=', '{', '[', '(', '?', '|', '&':
		return false
	case '/':
		return scanner.scanAutomaticSemicolonAfterSlash(cursor, validSymbols)
	case '+', '-':
		return true
	case '!':
		return cursor.advance(true) && lexer.Lookahead != '='
	case 'b':
		if validSymbols[kotlinByDelegationHint] {
			matched := kotlinScanForWord(cursor, "y")
			if cursor.limitHit {
				return false
			}
			return !matched
		}
		return true
	case 'e':
		matched := kotlinScanForWord(cursor, "lse")
		if cursor.limitHit {
			return false
		}
		if !matched {
			return true
		}
		return kotlinFollowedByArrow(cursor)
	case 'a':
		matched := kotlinScanForWord(cursor, "s")
		return !cursor.limitHit && !matched
	case 'w':
		matched := kotlinScanForWord(cursor, "here")
		return !cursor.limitHit && !matched
	case 'i', 'p':
		if validSymbols[kotlinPrimaryConstructorKeyword] &&
			!validSymbols[kotlinStringContent] &&
			kotlinCheckModifierThenConstructor(cursor) {
			return false
		}
		return !cursor.limitHit
	case 'c':
		if validSymbols[kotlinPrimaryConstructorKeyword] &&
			!validSymbols[kotlinStringContent] {
			if kotlinConsumeToken(cursor, "constructor") {
				lexer.ResultSymbol = treesitter.Symbol(kotlinPrimaryConstructorKeyword)
				lexer.MarkEnd()
				return true
			}
			return !cursor.limitHit
		}
		matched := kotlinScanForWord(cursor, "atch")
		return !cursor.limitHit && !matched
	case 'f':
		matched := kotlinScanForWord(cursor, "inally")
		return !cursor.limitHit && !matched
	case '@':
		if validSymbols[kotlinPrimaryConstructorKeyword] &&
			!validSymbols[kotlinStringContent] &&
			kotlinCheckAnnotationThenConstructor(cursor) {
			return false
		}
		return !cursor.limitHit
	case ';':
		if !cursor.advance(false) {
			return false
		}
		lexer.MarkEnd()
		return true
	default:
		return true
	}
}

func (scanner *kotlinScanner) scanAutomaticSemicolonAfterSlash(
	cursor *kotlinScanCursor,
	validSymbols []bool,
) bool {
	lexer := cursor.lexer
	if !cursor.advance(false) {
		return false
	}
	if lexer.Lookahead == '/' {
		if !cursor.advance(true) {
			return false
		}
		for !lexer.EOF() && lexer.Lookahead != '\n' && lexer.Lookahead != '\r' &&
			lexer.Lookahead != 0 {
			if !cursor.advance(true) {
				return false
			}
		}
		if !kotlinSkipWhitespaceAndComments(cursor) {
			return false
		}
		switch lexer.Lookahead {
		case '.', ',', ':', '*', '%', '>', '<', '=', '{', '[', '(', '?', '|', '&', '/':
			return false
		case '!':
			return cursor.advance(true) && lexer.Lookahead != '='
		case 'e':
			matched := kotlinScanForWord(cursor, "lse")
			if cursor.limitHit {
				return false
			}
			if matched {
				return kotlinFollowedByArrow(cursor)
			}
			return true
		case 'a':
			matched := kotlinScanForWord(cursor, "s")
			return !cursor.limitHit && !matched
		case 'w':
			matched := kotlinScanForWord(cursor, "here")
			return !cursor.limitHit && !matched
		case 'c':
			matched := kotlinScanForWord(cursor, "atch")
			return !cursor.limitHit && !matched
		case 'b':
			if validSymbols[kotlinByDelegationHint] {
				matched := kotlinScanForWord(cursor, "y")
				return !cursor.limitHit && !matched
			}
			return true
		case 'f':
			matched := kotlinScanForWord(cursor, "inally")
			return !cursor.limitHit && !matched
		default:
			return true
		}
	}
	if lexer.Lookahead != '*' || !cursor.advance(false) {
		return false
	}

	depth := 1
	afterStar := false
	for depth > 0 && !lexer.EOF() {
		switch lexer.Lookahead {
		case '*':
			if !cursor.advance(false) {
				return false
			}
			afterStar = true
		case '/':
			if !cursor.advance(false) {
				return false
			}
			if afterStar {
				afterStar = false
				depth--
			} else {
				if lexer.Lookahead == '*' {
					if depth == kotlinMaximumProbeDepth {
						return cursor.rejectLimit()
					}
					depth++
					if !cursor.advance(false) {
						return false
					}
				}
				afterStar = false
			}
		default:
			if !cursor.advance(false) {
				return false
			}
			afterStar = false
		}
	}
	if depth > 0 {
		// Match the pinned scanner's recovery behavior: an unterminated block
		// comment at EOF is still emitted as a comment token, rather than first
		// inserting a zero-width semicolon ahead of it.
		lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		lexer.MarkEnd()
		return true
	}
	if !cursor.skipWhitespace() {
		return false
	}
	switch lexer.Lookahead {
	case '.', ',', ':', '%', '>', '<', '=', '{', '[', '(', '?', '|', '&', '/', '*':
		lexer.MarkEnd()
		lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		return true
	case '!':
		lexer.MarkEnd()
		if !cursor.advance(true) {
			return false
		}
		if lexer.Lookahead == '=' {
			lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		}
		return true
	case 'e':
		lexer.MarkEnd()
		matched := kotlinScanForWord(cursor, "lse")
		if cursor.limitHit {
			return false
		}
		if matched {
			if kotlinFollowedByArrow(cursor) {
				return true
			}
			if cursor.limitHit {
				return false
			}
			lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		}
		return true
	case 'a':
		lexer.MarkEnd()
		matched := kotlinScanForWord(cursor, "s")
		if cursor.limitHit {
			return false
		}
		if matched {
			lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		}
		return true
	case 'w':
		lexer.MarkEnd()
		matched := kotlinScanForWord(cursor, "here")
		if cursor.limitHit {
			return false
		}
		if matched {
			lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
		}
		return true
	case 'b':
		if validSymbols[kotlinByDelegationHint] {
			lexer.MarkEnd()
			matched := kotlinScanForWord(cursor, "y")
			if cursor.limitHit {
				return false
			}
			if matched {
				lexer.ResultSymbol = treesitter.Symbol(kotlinMultilineComment)
			}
		}
		return true
	default:
		return true
	}
}

func (scanner *kotlinScanner) scanImportDot(cursor *kotlinScanCursor) bool {
	lexer := cursor.lexer
	if lexer.EOF() || lexer.Lookahead != '.' {
		return false
	}
	lexer.MarkEnd()
	if !cursor.advance(false) {
		return false
	}
	foundNewline := false
	for !lexer.EOF() && unicode.IsSpace(lexer.Lookahead) {
		if lexer.Lookahead == '\n' || lexer.Lookahead == '\r' {
			foundNewline = true
		}
		if !cursor.advance(true) {
			return false
		}
	}
	if foundNewline && lexer.Lookahead == 'i' &&
		kotlinScanForWord(cursor, "mport") {
		lexer.ResultSymbol = treesitter.Symbol(kotlinAutomaticSemicolon)
		return true
	}
	if cursor.limitHit {
		return false
	}
	lexer.ResultSymbol = treesitter.Symbol(kotlinImportDot)
	lexer.MarkEnd()
	return true
}
