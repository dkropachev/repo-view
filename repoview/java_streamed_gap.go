package repoview

// javaStreamedDefinitionBrace is the bounded structural state needed while
// recovering definitions from the retained lexer's omitted middle. A type
// kind makes its direct contents eligible as members. owners are output
// indices whose scope ends at this brace's matching close.
type javaStreamedDefinitionBrace struct {
	resumeHeader      []javaToken
	resumeOpaque      []javaByteSpan
	pendingStatement  []javaToken
	pendingOwners     []sourceDefinition
	typeKind          string
	typeName          string
	controlKind       string
	owners            []int
	openOffset        int
	resumeAttached    int
	scopeStart        int
	aggregateStart    int
	switchLabelStart  int
	switchLastEnd     int
	switchLastLine    int
	recoverContents   bool
	recoverScopes     bool
	enumConstants     bool
	emitScope         bool
	pendingScope      bool
	resumeOverflow    bool
	resumeDeclaration bool
	inlineInitializer bool
	switchBody        bool
	switchLabelColon  bool
}

type javaStreamedGapResult struct {
	definitions []sourceDefinition
	scopes      []javaLineScope
	imports     []javaLineSpan
}

type javaStreamedExpressionArrow struct {
	startOffset  int
	parenDepth   int
	bracketDepth int
	ternaryDepth int
	recover      bool
	switchRule   bool
}

// javaStreamedAttachedComments reduces an arbitrarily long trivia run to the
// exact documentation start that can attach to the next header token. Ordinary
// comments remain transparent, while Markdown documentation only groups with
// directly adjacent Markdown comments, matching javaLexicalAttachedStart.
type javaStreamedAttachedComments struct {
	lastEnd       int
	attachedStart int
	hasLast       bool
	hasAttached   bool
	lastMarkdown  bool
}

func (state *javaStreamedAttachedComments) reset() {
	*state = javaStreamedAttachedComments{}
}

func (state *javaStreamedAttachedComments) add(
	input *javaUnicodeInput,
	span javaByteSpan,
) {
	parts := javaTranslatedCommentParts(input, span)
	if len(parts) == 0 {
		parts = []javaByteSpan{span}
	}
	for _, part := range parts {
		if state.hasLast {
			whitespace, lineBreaks := javaTranslatedWhitespaceGap(
				input, state.lastEnd, part.start,
			)
			if !whitespace || lineBreaks > 1 {
				state.reset()
			}
		}
		markdown := javaTranslatedMarkdownJavadoc(input, part)
		switch {
		case markdown:
			if !state.hasAttached || !state.lastMarkdown {
				state.attachedStart = part.start
			}
			state.hasAttached = true
		case javaTranslatedJavadoc(input, part):
			if !state.hasAttached {
				state.attachedStart = part.start
				state.hasAttached = true
			}
		}
		state.lastEnd = part.end
		state.hasLast = true
		state.lastMarkdown = markdown
	}
}

func (state *javaStreamedAttachedComments) start(
	input *javaUnicodeInput,
	tokenStart int,
) int {
	if !state.hasLast || !state.hasAttached {
		return tokenStart
	}
	whitespace, lineBreaks := javaTranslatedWhitespaceGap(
		input, state.lastEnd, tokenStart,
	)
	if !whitespace || lineBreaks > 1 {
		return tokenStart
	}
	return state.attachedStart
}

// javaStreamedGapDefinitions recovers declaration headers from the exact
// lexical stream without retaining the omitted token range. Headers are
// disjoint segments bounded by braces or semicolons, so the total analysis is
// linear; an individual malformed header is capped at the recovery budget.
func javaStreamedGapDefinitions(
	source string,
	lineCount int,
	lexed javaLexResult,
) []sourceDefinition {
	result := analyzeJavaStreamedGapMode(source, lineCount, lexed, false)
	return result.definitions
}

func analyzeJavaStreamedGap(
	source string,
	lineCount int,
	lexed javaLexResult,
) javaStreamedGapResult {
	return analyzeJavaStreamedGapMode(source, lineCount, lexed, true)
}

func analyzeJavaStreamedGapMode(
	source string,
	lineCount int,
	lexed javaLexResult,
	recoverCrossBoundary bool,
) javaStreamedGapResult {
	if lexed.input == nil || !lexed.truncated {
		return javaStreamedGapResult{}
	}
	gapStart, gapEnd, ok := javaStoredTokenGapRange(lexed.tokens)
	if !ok {
		return javaStreamedGapResult{}
	}

	positions := javaSourcePositions{
		source: source, lineStarts: javaLineStarts(source),
	}
	definitions := make([]sourceDefinition, 0)
	scopes := make([]javaLineScope, 0)
	imports := make([]javaLineSpan, 0)
	braces := make([]javaStreamedDefinitionBrace, 0, 32)
	braceOverflow := 0
	header := make([]javaToken, 0, javaMaximumRecoveryHeaderTokens+2)
	headerOpaque := make([]javaByteSpan, 0, 4)
	headerOverflow := false
	headerAttachedStart := -1
	attachedComments := javaStreamedAttachedComments{}
	parenDepth := 0
	bracketDepth := 0
	annotationParens := make([]int, 0, 4)
	inlineAnnotationBraces := 0
	gapEntered := false
	suspendedHeaderTokens := 0
	pendingOwnerCount := 0
	pendingScopeCount := 0
	expressionArrows := make([]javaStreamedExpressionArrow, 0, 4)
	pendingIfTokens := make([]javaToken, 0, 32)
	pendingIfTokenDepth := -1
	pendingIfStart := 0
	pendingIfDepth := -1
	pendingTryStart := 0
	pendingTryEnd := 0
	pendingTryDepth := -1
	pendingDoStart := 0
	pendingDoDepth := -1
	currentAggregateStart := 0

	directTypeKind := func() string {
		if braceOverflow > 0 || len(braces) == 0 {
			return ""
		}
		return braces[len(braces)-1].typeKind
	}
	directTypeRecovery := func() bool {
		return braceOverflow == 0 && len(braces) > 0 &&
			braces[len(braces)-1].recoverContents
	}
	directTypeName := func() string {
		if braceOverflow > 0 || len(braces) == 0 {
			return ""
		}
		return braces[len(braces)-1].typeName
	}
	directEnumConstants := func() bool {
		return braceOverflow == 0 && len(braces) > 0 &&
			braces[len(braces)-1].typeKind == "enum" &&
			braces[len(braces)-1].enumConstants
	}
	atCompilationRoot := func() bool {
		return braceOverflow == 0 && len(braces) == 0
	}
	resetHeader := func() {
		header = header[:0]
		headerOpaque = headerOpaque[:0]
		headerOverflow = false
		headerAttachedStart = -1
		attachedComments.reset()
		parenDepth = 0
		bracketDepth = 0
		annotationParens = annotationParens[:0]
		inlineAnnotationBraces = 0
		expressionArrows = expressionArrows[:0]
		currentAggregateStart = 0
	}
	startNestedHeader := func() {
		header = make([]javaToken, 0, 16)
		headerOpaque = nil
		headerOverflow = false
		headerAttachedStart = -1
		attachedComments.reset()
		parenDepth = 0
		bracketDepth = 0
		annotationParens = nil
		inlineAnnotationBraces = 0
		expressionArrows = nil
		currentAggregateStart = 0
	}
	appendHeader := func(token javaToken) {
		if headerOverflow {
			return
		}
		if len(header) >= javaMaximumRecoveryHeaderTokens {
			headerOverflow = true
			return
		}
		header = append(header, token)
	}
	definitionOffset := func(definition sourceDefinition) int {
		if definition.line < 1 || definition.line > len(positions.lineStarts) ||
			definition.column < 1 {
			return -1
		}
		return positions.lineStarts[definition.line-1] + definition.column - 1
	}
	headerTouchesGap := func(tokens []javaToken) bool {
		for _, token := range tokens {
			if token.end > gapStart && token.start < gapEnd {
				return true
			}
		}
		return false
	}

	// analyzeHeader reuses ordinary compact recovery on one bounded header.
	// open is non-nil for a body opener; a synthetic close lets recovery prove
	// the declaration immediately, while the real close later supplies scopeEnd.
	analyzeHeader := func(
		open *javaToken,
		allowMembers bool,
		declarationHeader bool,
		force bool,
	) (owners []int, pendingOwners []sourceDefinition) {
		if headerOverflow || len(header) == 0 ||
			(!allowMembers && !declarationHeader) ||
			!javaStreamedHeaderMayDeclare(header, declarationHeader) {
			return nil, nil
		}
		crossesGap := force || headerTouchesGap(header)
		if !crossesGap && (open == nil || !recoverCrossBoundary) {
			return nil, nil
		}
		if open == nil && allowMembers {
			if name, simple := javaStreamedSimpleFieldName(header); simple &&
				!javaStreamedFieldHeaderHasIllegalOpaque(header, headerOpaque) &&
				(name.start >= gapStart && name.start < gapEnd || crossesGap) {
				line, column := positions.lineColumn(name.start)
				definitions = append(definitions, sourceDefinition{
					symbol: name.text, line: line, column: column,
					scopeStart: line, scopeEnd: line,
				})
				return nil, nil
			}
		}
		tokens := append([]javaToken(nil), header...)
		if open != nil {
			tokens = append(tokens, javaToken{
				text: "}", value: "}", start: open.end, end: open.end,
			})
			if javaStreamedHeaderNeedsTerminator(header) {
				tokens = append(tokens, javaToken{
					text: ";", value: ";", start: open.end, end: open.end,
				})
			}
		}
		local := analyzeJavaLexicallyWithPositionsMode(
			source, lineCount, javaLexResult{
				input: lexed.input, tokens: tokens,
				stringSpans: append([]javaByteSpan(nil), headerOpaque...),
			}, positions, false,
		)
		headerHasScopedInitializer := javaStreamedHeaderHasScopedInitializer(header)
		for _, definition := range local.definitions {
			offset := definitionOffset(definition)
			if (offset < gapStart || offset >= gapEnd) && !crossesGap && open == nil {
				continue
			}
			// Outside a type body, only an explicit type or module header is
			// a Java source definition. This prevents compact recovery from
			// promoting typed locals and expression statements to fields.
			if !allowMembers && !declarationHeader {
				continue
			}
			if open == nil && definition.ownsScope &&
				javaStreamedHeaderContains(header, "=") &&
				!javaStreamedHeaderContains(header, "default") &&
				!headerHasScopedInitializer {
				definition.ownsScope = false
				definition.scopeStart = definition.line
				definition.scopeEnd = definition.line
			}
			if definition.ownsScope && headerAttachedStart >= 0 {
				definition.scopeStart, _ = positions.lineColumn(headerAttachedStart)
			}
			if !crossesGap {
				if definition.ownsScope {
					pendingOwners = append(pendingOwners, definition)
				}
				continue
			}
			definitions = append(definitions, definition)
			index := len(definitions) - 1
			if definition.ownsScope {
				if open != nil {
					owners = append(owners, index)
				} else {
					scopes = append(scopes, javaLineScope{
						start: definition.scopeStart, end: definition.scopeEnd,
					})
				}
			}
		}
		return owners, pendingOwners
	}
	tokenDefinition := func(
		name javaToken,
		ownsScope bool,
		force bool,
	) (sourceDefinition, bool) {
		if !javaTokenIsSourceName(name) ||
			(!force && !headerTouchesGap(header) && !directTypeRecovery()) {
			return sourceDefinition{}, false
		}
		line, column := positions.lineColumn(name.start)
		scopeStart := line
		if ownsScope && headerAttachedStart >= 0 {
			scopeStart, _ = positions.lineColumn(headerAttachedStart)
		}
		return sourceDefinition{
			symbol: name.text, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: line, ownsScope: ownsScope,
		}, true
	}
	appendTokenDefinition := func(name javaToken, ownsScope bool) int {
		definition, ok := tokenDefinition(name, ownsScope, false)
		if !ok {
			return -1
		}
		definitions = append(definitions, definition)
		return len(definitions) - 1
	}
	appendEnumConstant := func(ownsScope bool) int {
		if javaStreamedEnumHeaderHasIllegalOpaque(header, headerOpaque) {
			return -1
		}
		name, ok := javaStreamedEnumConstantName(header)
		if !ok {
			return -1
		}
		return appendTokenDefinition(name, ownsScope)
	}
	appendImport := func(moduleDirective bool) {
		if !javaStreamedImportHeader(header, moduleDirective) ||
			javaStreamedHeaderHasIllegalOpaque(header, headerOpaque, nil, false) ||
			(!headerTouchesGap(header) &&
				(!moduleDirective || !directTypeRecovery())) {
			return
		}
		startOffset := header[0].start
		if headerAttachedStart >= 0 {
			startOffset = headerAttachedStart
		}
		startLine, endLine := positions.lineSpan(
			startOffset, header[len(header)-1].end,
		)
		imports = append(imports, javaLineSpan{start: startLine, end: endLine})
	}
	emitExpressionArrows := func(endOffset int, boundary string) {
		retained := expressionArrows[:0]
		for _, arrow := range expressionArrows {
			closeArrow := false
			switch boundary {
			case ";":
				closeArrow = parenDepth <= arrow.parenDepth &&
					bracketDepth <= arrow.bracketDepth
			case ",":
				closeArrow = parenDepth == arrow.parenDepth &&
					bracketDepth == arrow.bracketDepth
			case ":":
				if arrow.ternaryDepth > 0 {
					arrow.ternaryDepth--
				} else {
					closeArrow = parenDepth == arrow.parenDepth &&
						bracketDepth == arrow.bracketDepth
				}
			case ")":
				closeArrow = parenDepth < arrow.parenDepth
			case "]":
				closeArrow = bracketDepth < arrow.bracketDepth
			case "}":
				closeArrow = parenDepth == arrow.parenDepth &&
					bracketDepth == arrow.bracketDepth
			}
			if closeArrow {
				if arrow.recover && endOffset >= arrow.startOffset {
					startLine, endLine := positions.lineSpan(arrow.startOffset, endOffset)
					scopes = append(scopes, javaLineScope{start: startLine, end: endLine})
				}
				continue
			}
			retained = append(retained, arrow)
		}
		expressionArrows = retained
	}
	enterGap := func(eventEnd int) {
		if gapEntered || eventEnd <= gapStart {
			return
		}
		gapEntered = true
		for index := range braces {
			if javaStreamedDefinitionTypeBody(braces[index].typeKind) ||
				braces[index].typeKind == "module" {
				braces[index].recoverContents = true
			}
		}
	}
	appendSwitchLabelScope := func(context *javaStreamedDefinitionBrace, endOffset int) {
		if context == nil || !context.switchLabelColon || context.switchLabelStart < 0 ||
			endOffset < context.switchLabelStart || !context.recoverScopes {
			return
		}
		startLine, endLine := positions.lineSpan(context.switchLabelStart, endOffset)
		if context.switchLastLine >= startLine {
			endLine = context.switchLastLine
		}
		scopes = append(scopes, javaLineScope{start: startLine, end: endLine})
	}
	appendUnbracedScopes := func() {
		if len(header) < 2 || header[len(header)-1].value != ";" ||
			(!headerTouchesGap(header) && !directTypeRecovery()) {
			return
		}
		endLine, _ := positions.lineColumn(max(
			header[len(header)-1].start, header[len(header)-1].end-1,
		))
		depth := len(braces)
		if pendingDoDepth == depth {
			for _, candidate := range header[:len(header)-1] {
				if candidate.value == "while" {
					scopes = append(scopes, javaLineScope{
						start: pendingDoStart, end: endLine,
					})
					pendingDoStart, pendingDoDepth = 0, -1
					return
				}
			}
		}
		for cursor := 0; cursor+1 < len(header)-1 &&
			javaTokenIsSourceName(header[cursor]) && header[cursor+1].value == ":"; cursor += 2 {
			labelLine, _ := positions.lineColumn(header[cursor].start)
			scopes = append(scopes, javaLineScope{start: labelLine, end: endLine})
		}
		ifStart := 0
		elseStart := 0
		doStart := 0
		for _, candidate := range header[:len(header)-1] {
			line, _ := positions.lineColumn(candidate.start)
			switch candidate.value {
			case "if":
				scopes = append(scopes, javaLineScope{start: line, end: endLine})
				if ifStart == 0 {
					ifStart = line
				}
			case "while", "for", "synchronized":
				scopes = append(scopes, javaLineScope{start: line, end: endLine})
			case "else":
				elseStart = line
				scopes = append(scopes, javaLineScope{start: line, end: endLine})
			case "do":
				doStart = line
			}
		}
		if doStart > 0 {
			pendingDoStart, pendingDoDepth = doStart, depth
		}
		if elseStart > 0 && currentAggregateStart > 0 {
			scopes = append(scopes, javaLineScope{
				start: currentAggregateStart, end: endLine,
			})
		}
		if ifStart > 0 {
			pendingIfStart = ifStart
			if currentAggregateStart > 0 {
				pendingIfStart = currentAggregateStart
			}
			pendingIfDepth = depth
		} else if elseStart > 0 {
			pendingIfStart, pendingIfDepth = 0, -1
		}
	}
	finalizePendingIfTokens := func() {
		if len(pendingIfTokens) == 0 {
			pendingIfTokenDepth = -1
			return
		}
		local := analyzeJavaLexicallyWithPositionsMode(
			source, lineCount, javaLexResult{
				input: lexed.input, tokens: pendingIfTokens,
			}, positions, false,
		)
		scopes = append(scopes, local.scopes...)
		pendingIfTokens = pendingIfTokens[:0]
		pendingIfTokenDepth = -1
	}
	appendPendingIfTokens := func(tokens []javaToken, depth int) bool {
		if len(tokens) > javaMaximumRecoveryHeaderTokens-len(pendingIfTokens) {
			pendingIfTokens = pendingIfTokens[:0]
			pendingIfTokenDepth = -1
			return false
		}
		pendingIfTokens = append(pendingIfTokens, tokens...)
		pendingIfTokenDepth = depth
		return true
	}

	streamJavaLexicalEventsFromInput(
		lexed.input, javaLexicalStreamOptions{comments: true},
		func(event javaLexicalStreamEvent) bool {
			eventEnd := event.span.end
			if event.kind == javaLexicalStreamToken {
				eventEnd = event.token.end
			}
			enterGap(eventEnd)
			if event.kind == javaLexicalStreamComment {
				if len(header) == 0 && !headerOverflow {
					attachedComments.add(lexed.input, event.span)
				}
				return true
			}
			if event.kind == javaLexicalStreamOpaque {
				if !headerOverflow && len(header) < javaMaximumRecoveryHeaderTokens {
					// A literal is an atomic expression boundary. A numeric-shaped
					// placeholder prevents identifiers on its two sides from becoming
					// adjacent without retaining the literal spelling.
					header = append(header, javaToken{
						text: "0", value: "0", start: event.span.start,
						end: event.span.end,
					})
					headerOpaque = append(headerOpaque, event.span)
				} else {
					headerOverflow = true
				}
				return true
			}
			if event.kind != javaLexicalStreamToken {
				return true
			}
			token := event.token
			if len(header) == 0 {
				depth := len(braces)
				if pendingIfTokenDepth == depth && len(pendingIfTokens) > 0 &&
					!javaStreamedPendingStatementAccepts(pendingIfTokens, token.value) {
					finalizePendingIfTokens()
				}
				if pendingIfDepth == depth {
					if token.value == "else" {
						currentAggregateStart = pendingIfStart
					} else {
						pendingIfStart, pendingIfDepth = 0, -1
					}
				}
				if pendingTryDepth == depth {
					if token.value == "catch" || token.value == "finally" {
						currentAggregateStart = pendingTryStart
					} else {
						if pendingTryStart > 0 && pendingTryEnd >= pendingTryStart {
							scopes = append(scopes, javaLineScope{
								start: pendingTryStart, end: pendingTryEnd,
							})
						}
						pendingTryStart, pendingTryEnd, pendingTryDepth = 0, 0, -1
					}
				}
			}
			if braceOverflow == 0 && len(braces) > 0 && token.value != "}" &&
				braces[len(braces)-1].switchBody {
				switchContext := &braces[len(braces)-1]
				combinedDefault := token.value == "default" && len(header) > 0 &&
					header[len(header)-1].value == ","
				if (token.value == "case" || token.value == "default") && !combinedDefault {
					appendSwitchLabelScope(switchContext, switchContext.switchLastEnd)
					switchContext.switchLabelStart = token.start
					switchContext.switchLabelColon = false
				}
				switchContext.switchLastEnd = token.end
				switchContext.switchLastLine, _ = positions.lineColumn(
					max(token.start, token.end-1),
				)
			}
			if len(header) == 0 && !headerOverflow {
				headerAttachedStart = attachedComments.start(lexed.input, token.start)
			}
			appendHeader(token)

			switch token.value {
			case "(":
				nameIndex := len(header) - 2
				if nameIndex >= 0 && javaLexicalAnnotationName(header, nameIndex) &&
					len(annotationParens) < javaMaximumRecoveryHeaderTokens {
					annotationParens = append(annotationParens, parenDepth+1)
				}
				parenDepth++
			case ")":
				parenDepth = max(0, parenDepth-1)
				for len(annotationParens) > 0 &&
					annotationParens[len(annotationParens)-1] > parenDepth {
					annotationParens = annotationParens[:len(annotationParens)-1]
				}
				if len(header) >= 2 {
					emitExpressionArrows(header[len(header)-2].end, ")")
				}
			case "[":
				bracketDepth++
			case "]":
				bracketDepth = max(0, bracketDepth-1)
				if len(header) >= 2 {
					emitExpressionArrows(header[len(header)-2].end, "]")
				}
			case "?":
				for index := range expressionArrows {
					expressionArrows[index].ternaryDepth++
				}
			case "->":
				startIndex := len(header) - 2
				switchRule := braceOverflow == 0 && len(braces) > 0 &&
					braces[len(braces)-1].switchBody
				if switchRule {
					for index := range len(header) - 1 {
						if header[index].value == "case" || header[index].value == "default" {
							startIndex = index
							break
						}
					}
				} else if startIndex >= 0 && header[startIndex].value == ")" {
					depth := 0
					for index := startIndex; index >= 0; index-- {
						switch header[index].value {
						case ")":
							depth++
						case "(":
							depth--
							if depth == 0 {
								startIndex = index
								index = -1
							}
						}
					}
				}
				if startIndex >= 0 && len(expressionArrows) < javaMaximumRecoveryHeaderTokens {
					expressionArrows = append(expressionArrows, javaStreamedExpressionArrow{
						startOffset: header[startIndex].start,
						parenDepth:  parenDepth, bracketDepth: bracketDepth,
						recover: switchRule || directTypeRecovery() ||
							header[startIndex].end > gapStart && header[startIndex].start < gapEnd,
						switchRule: switchRule,
					})
				}
			case ":":
				if braceOverflow == 0 && len(braces) > 0 && token.value != "}" &&
					braces[len(braces)-1].switchBody {
					braces[len(braces)-1].switchLabelColon = true
				}
				if len(header) >= 2 {
					emitExpressionArrows(header[len(header)-2].end, ":")
				}
			case "{":
				if len(header) >= 2 && header[len(header)-2].value == "->" &&
					len(expressionArrows) > 0 {
					expressionArrows = expressionArrows[:len(expressionArrows)-1]
				}
				if inlineAnnotationBraces > 0 || len(annotationParens) > 0 &&
					parenDepth >= annotationParens[len(annotationParens)-1] {
					inlineAnnotationBraces++
					return true
				}
				parentSwitch := braceOverflow == 0 && len(braces) > 0 &&
					braces[len(braces)-1].switchBody
				initializerBody := javaStreamedInitializerBodyHeader(
					header, directEnumConstants() && parenDepth == 0,
				)
				if parentSwitch && len(header) >= 2 && header[len(header)-2].value == "->" {
					initializerBody = false
				}
				insideArrayInitializer := braceOverflow == 0 && len(braces) > 0 &&
					braces[len(braces)-1].inlineInitializer
				inlineInitializer := javaStreamedArrayInitializerHeader(header) ||
					insideArrayInitializer && !initializerBody
				kind, declarationHeader := javaStreamedDefinitionHeaderKind(header)
				if declarationHeader && javaStreamedHeaderHasIllegalOpaque(
					header, headerOpaque, nil, true,
				) {
					kind = ""
					declarationHeader = false
				}
				currentKind := directTypeKind()
				allowMembers := javaStreamedDefinitionTypeBody(currentKind) ||
					atCompilationRoot()
				recoverContents := directTypeRecovery()
				directEnums := directEnumConstants()
				enumConstants := directEnums && parenDepth == 0
				nestedEnumConstant := directEnums && parenDepth > 0
				if nestedEnumConstant {
					appendEnumConstant(false)
				}
				headerMembers := allowMembers && !nestedEnumConstant
				continuation := inlineInitializer || initializerBody
				var owners []int
				var pendingOwners []sourceDefinition
				if !continuation {
					owners, pendingOwners = analyzeHeader(
						&token, headerMembers, declarationHeader, recoverContents,
					)
				}
				if !continuation && !enumConstants && allowMembers && !declarationHeader &&
					!javaStreamedHeaderHasIllegalOpaque(
						header, headerOpaque, nil, true,
					) {
					if constructor, ok := javaStreamedSimpleConstructorName(
						header, directTypeName(),
					); ok {
						if owner := appendTokenDefinition(constructor, true); owner >= 0 {
							owners = append(owners, owner)
						} else if definition, ok := tokenDefinition(
							constructor, true, true,
						); ok {
							pendingOwners = append(pendingOwners, definition)
						}
					}
				}
				if !continuation && enumConstants {
					if owner := appendEnumConstant(true); owner >= 0 {
						owners = append(owners, owner)
					}
				}
				if kind == "" && javaStreamedAnonymousTypeHeader(
					header, enumConstants,
				) {
					kind = "anonymous"
				}
				context := javaStreamedDefinitionBrace{
					typeKind:          kind,
					typeName:          javaStreamedDeclarationTypeName(header, kind),
					openOffset:        token.start,
					inlineInitializer: inlineInitializer,
					switchBody:        javaStreamedHeaderContains(header, "switch"),
					controlKind:       javaStreamedControlBraceKind(header),
					aggregateStart:    currentAggregateStart,
					switchLabelStart:  -1,
					recoverContents: (javaStreamedDefinitionTypeBody(kind) || kind == "module") &&
						(recoverContents || headerTouchesGap(header)),
					enumConstants: kind == "enum",
					owners:        owners,
					pendingOwners: pendingOwners,
				}
				statementHeader := javaStreamedStatementHeader(header) ||
					pendingIfTokenDepth == len(braces) && len(pendingIfTokens) > 0
				if statementHeader && len(header)+len(pendingIfTokens) <=
					javaMaximumRecoveryHeaderTokens-pendingScopeCount {
					context.pendingStatement = make(
						[]javaToken, 0, len(header)+len(pendingIfTokens)+1,
					)
					context.pendingStatement = append(
						context.pendingStatement, pendingIfTokens...,
					)
					context.pendingStatement = append(context.pendingStatement, header...)
					pendingScopeCount += len(context.pendingStatement)
					pendingIfTokens = pendingIfTokens[:0]
					pendingIfTokenDepth = -1
				}
				if anchor := javaStreamedBraceScopeAnchor(header, kind, parentSwitch); anchor >= 0 {
					scopeOffset := header[anchor].start
					if anchor == 0 && header[anchor].value == "static" &&
						headerAttachedStart >= 0 {
						scopeOffset = headerAttachedStart
					}
					context.scopeStart, _ = positions.lineColumn(scopeOffset)
				}
				context.emitScope = !inlineInitializer && len(owners) == 0 &&
					context.scopeStart > 0 &&
					(headerTouchesGap(header) || recoverContents)
				context.recoverScopes = context.emitScope
				if len(context.pendingStatement) > 0 {
					context.emitScope = false
				}
				context.pendingScope = !inlineInitializer && len(owners) == 0 &&
					len(pendingOwners) == 0 && context.scopeStart > 0
				if len(pendingOwners) > javaMaximumRecoveryHeaderTokens-pendingOwnerCount {
					context.pendingOwners = nil
					context.pendingScope = false
				} else {
					pendingOwnerCount += len(pendingOwners)
				}
				if continuation && len(header) <=
					javaMaximumRecoveryHeaderTokens-suspendedHeaderTokens {
					context.resumeHeader = header
					context.resumeOpaque = headerOpaque
					context.resumeAttached = headerAttachedStart
					context.resumeOverflow = headerOverflow
					context.resumeDeclaration = true
					suspendedHeaderTokens += len(header)
				}
				if braceOverflow > 0 || len(braces) >= javaMaximumRecoveryHeaderTokens {
					braceOverflow++
				} else {
					braces = append(braces, context)
				}
				if context.resumeDeclaration {
					startNestedHeader()
				} else {
					resetHeader()
				}
			case "}":
				if inlineAnnotationBraces > 0 {
					inlineAnnotationBraces--
					return true
				}
				if braceOverflow == 0 && len(braces) > 0 &&
					braces[len(braces)-1].inlineInitializer && len(header) >= 2 {
					emitExpressionArrows(header[len(header)-2].end, "}")
				}
				if braceOverflow > 0 {
					braceOverflow--
				} else if len(braces) > 0 {
					context := braces[len(braces)-1]
					braces = braces[:len(braces)-1]
					pendingScopeCount -= len(context.pendingStatement)
					pendingOwnerCount -= len(context.pendingOwners)
					endLine, _ := positions.lineColumn(max(token.start, token.end-1))
					exactEnd := javaTokenIsExactPunctuation(lexed.input, token, '}')
					if context.switchBody {
						appendSwitchLabelScope(&context, context.switchLastEnd)
					}
					spansGap := context.openOffset < gapStart && token.end > gapEnd
					if recoverCrossBoundary && spansGap {
						for _, definition := range context.pendingOwners {
							definition.scopeEnd = max(definition.line, endLine)
							definition.ownedEndColumn = javaExactOwnedEndColumn(
								positions, definition.scopeEnd, token.end, exactEnd,
							)
							definitions = append(definitions, definition)
							scopes = append(scopes, javaLineScope{
								start: definition.scopeStart, end: definition.scopeEnd,
							})
						}
					}
					for _, owner := range context.owners {
						if owner < 0 || owner >= len(definitions) {
							continue
						}
						definitions[owner].scopeEnd = max(
							definitions[owner].line, endLine,
						)
						definitions[owner].ownedEndColumn = javaExactOwnedEndColumn(
							positions, definitions[owner].scopeEnd, token.end, exactEnd,
						)
						scopes = append(scopes, javaLineScope{
							start: definitions[owner].scopeStart,
							end:   definitions[owner].scopeEnd,
						})
					}
					if context.emitScope ||
						recoverCrossBoundary && spansGap && context.pendingScope {
						scopes = append(scopes, javaLineScope{
							start: context.scopeStart, end: endLine,
						})
					}
					if len(context.pendingStatement) == 0 {
						switch context.controlKind {
						case "if":
							pendingIfStart = context.scopeStart
							if context.aggregateStart > 0 {
								pendingIfStart = context.aggregateStart
							}
							pendingIfDepth = len(braces)
						case "else":
							if context.aggregateStart > 0 {
								scopes = append(scopes, javaLineScope{
									start: context.aggregateStart, end: endLine,
								})
							}
							pendingIfStart, pendingIfDepth = 0, -1
						case "try", "catch":
							pendingTryStart = context.scopeStart
							if context.aggregateStart > 0 {
								pendingTryStart = context.aggregateStart
							}
							pendingTryDepth = len(braces)
							pendingTryEnd = endLine
						case "do":
							pendingDoStart, pendingDoDepth = context.scopeStart, len(braces)
						case "finally":
							if context.aggregateStart > 0 {
								scopes = append(scopes, javaLineScope{
									start: context.aggregateStart, end: endLine,
								})
							}
							pendingTryStart, pendingTryEnd, pendingTryDepth = 0, 0, -1
						}
					} else if len(context.pendingStatement) < javaMaximumRecoveryHeaderTokens {
						pendingIfTokens = append(
							pendingIfTokens[:0], context.pendingStatement...,
						)
						pendingIfTokens = append(pendingIfTokens, token)
						pendingIfTokenDepth = len(braces)
					}
					if context.resumeDeclaration {
						innerHeader := header
						innerOpaque := headerOpaque
						suspendedHeaderTokens -= len(context.resumeHeader)
						header = context.resumeHeader
						headerOpaque = context.resumeOpaque
						headerAttachedStart = context.resumeAttached
						headerOverflow = context.resumeOverflow
						parenDepth = 0
						bracketDepth = 0
						annotationParens = nil
						inlineAnnotationBraces = 0
						for _, innerToken := range innerHeader {
							appendHeader(innerToken)
						}
						if !headerOverflow && len(headerOpaque)+len(innerOpaque) <=
							javaMaximumRecoveryHeaderTokens {
							headerOpaque = append(headerOpaque, innerOpaque...)
						}
						appendHeader(token)
						return true
					}
					if len(braces) > 0 && braces[len(braces)-1].switchBody {
						braces[len(braces)-1].switchLastEnd = token.end
						braces[len(braces)-1].switchLastLine = endLine
					}
				}
				resetHeader()
			case ",":
				if len(header) >= 2 {
					emitExpressionArrows(header[len(header)-2].end, ",")
				}
				if parenDepth == 0 && bracketDepth == 0 && directEnumConstants() {
					appendEnumConstant(false)
					resetHeader()
				}
			case ";":
				if parenDepth == 0 && bracketDepth == 0 {
					if len(header) >= 2 {
						emitExpressionArrows(header[len(header)-2].end, ";")
					}
					pendingStatement := pendingIfTokenDepth == len(braces) &&
						len(pendingIfTokens) > 0 || javaStreamedStatementHeader(header)
					if pendingStatement {
						_ = appendPendingIfTokens(header, len(braces))
					} else {
						appendUnbracedScopes()
					}
					if atCompilationRoot() {
						appendImport(false)
					} else if directTypeKind() == "module" {
						appendImport(true)
					}
					if directEnumConstants() {
						appendEnumConstant(false)
						if braceOverflow == 0 && len(braces) > 0 {
							braces[len(braces)-1].enumConstants = false
						}
					} else {
						_, _ = analyzeHeader(
							nil, javaStreamedDefinitionTypeBody(directTypeKind()) ||
								atCompilationRoot(), false,
							directTypeRecovery(),
						)
					}
					resetHeader()
				}
			}
			return true
		},
	)

	for _, context := range braces {
		for _, owner := range context.owners {
			if owner >= 0 && owner < len(definitions) {
				scopes = append(scopes, javaLineScope{
					start: definitions[owner].scopeStart,
					end:   definitions[owner].scopeEnd,
				})
			}
		}
		if context.emitScope {
			scopes = append(scopes, javaLineScope{
				start: context.scopeStart, end: context.scopeStart,
			})
		}
	}
	return javaStreamedGapResult{
		definitions: sortUniqueJavaTreeDefinitions(definitions),
		scopes:      normalizeJavaLineScopes(scopes, lineCount),
		imports:     normalizeJavaLineSpans(imports, lineCount),
	}
}

func javaStoredTokenGapRange(tokens []javaToken) (int, int, bool) {
	for index, token := range tokens {
		if !token.gap {
			continue
		}
		start := 0
		if index > 0 {
			start = tokens[index-1].end
		}
		if start <= token.start {
			return start, token.start, true
		}
	}
	return 0, 0, false
}

func javaStreamedHeaderHasIllegalOpaque(
	tokens []javaToken,
	opaqueSpans []javaByteSpan,
	extraAllowed []javaByteSpan,
	allowAnnotations bool,
) bool {
	if len(opaqueSpans) == 0 {
		return false
	}
	if len(tokens) < 2 {
		return true
	}
	boundary := len(tokens) - 1
	delimiters := analyzeJavaDelimiters(tokens)
	return javaDeclarationHeaderHasIllegalOpaque(
		tokens, delimiters, 0, boundary,
		javaByteSpan{start: tokens[0].start, end: tokens[boundary].start},
		opaqueSpans, extraAllowed, allowAnnotations,
	)
}

func javaStreamedFieldHeaderHasIllegalOpaque(
	tokens []javaToken,
	opaqueSpans []javaByteSpan,
) bool {
	if len(opaqueSpans) == 0 {
		return false
	}
	if len(tokens) < 2 {
		return true
	}
	boundary := len(tokens) - 1
	delimiters := analyzeJavaDelimiters(tokens)
	headerEnd := javaFieldHeaderEnd(tokens, delimiters, 0, boundary)
	var allowed [1]javaByteSpan
	extraAllowed := allowed[:0]
	if headerEnd < boundary && tokens[headerEnd].value == "=" {
		extraAllowed = append(extraAllowed, javaByteSpan{
			start: tokens[headerEnd].end,
			end:   tokens[boundary].start,
		})
	}
	return javaDeclarationHeaderHasIllegalOpaque(
		tokens, delimiters, 0, boundary,
		javaByteSpan{start: tokens[0].start, end: tokens[boundary].start},
		opaqueSpans, extraAllowed, true,
	)
}

func javaStreamedEnumHeaderHasIllegalOpaque(
	tokens []javaToken,
	opaqueSpans []javaByteSpan,
) bool {
	if len(opaqueSpans) == 0 {
		return false
	}
	if len(tokens) < 2 {
		return true
	}
	name, ok := javaStreamedEnumConstantName(tokens)
	if !ok {
		return true
	}
	delimiters := analyzeJavaDelimiters(tokens)
	boundary := len(tokens) - 1
	var allowed [1]javaByteSpan
	extraAllowed := allowed[:0]
	for index := range boundary {
		if tokens[index].start != name.start || tokens[index].end != name.end {
			continue
		}
		argumentOpen := index + 1
		if argumentOpen < boundary && tokens[argumentOpen].value == "(" {
			if closeIndex := javaDelimiterMatch(delimiters, argumentOpen); closeIndex > argumentOpen &&
				closeIndex < boundary {
				extraAllowed = append(extraAllowed, javaByteSpan{
					start: tokens[argumentOpen].end,
					end:   tokens[closeIndex].start,
				})
			}
		}
		break
	}
	return javaDeclarationHeaderHasIllegalOpaque(
		tokens, delimiters, 0, boundary,
		javaByteSpan{start: tokens[0].start, end: tokens[boundary].start},
		opaqueSpans, extraAllowed, true,
	)
}

func javaStreamedDefinitionTypeBody(kind string) bool {
	switch kind {
	case "class", "interface", "enum", "record", "anonymous":
		return true
	default:
		return false
	}
}

func javaStreamedDefinitionHeaderKind(tokens []javaToken) (string, bool) {
	end := len(tokens)
	if end > 0 && tokens[end-1].value == "{" {
		end--
	}
	for index := range end {
		switch tokens[index].value {
		case "class", "interface", "enum", "record":
			if (index == 0 || tokens[index-1].value != ".") &&
				index+1 < end && javaTokenIsSourceName(tokens[index+1]) {
				return tokens[index].value, true
			}
		case "module":
			if index+1 < end && javaTokenIsSourceName(tokens[index+1]) {
				return "module", true
			}
		}
	}
	return "", false
}

func javaStreamedDeclarationTypeName(tokens []javaToken, kind string) string {
	if !javaStreamedDefinitionTypeBody(kind) || kind == "anonymous" {
		return ""
	}
	end := len(tokens)
	if end > 0 && tokens[end-1].value == "{" {
		end--
	}
	for index := 0; index+1 < end; index++ {
		if tokens[index].value == kind &&
			(index == 0 || tokens[index-1].value != ".") &&
			javaTokenIsSourceName(tokens[index+1]) {
			return tokens[index+1].value
		}
	}
	return ""
}

func javaStreamedSimpleConstructorName(
	tokens []javaToken,
	typeName string,
) (javaToken, bool) {
	if typeName == "" || len(tokens) < 2 || tokens[len(tokens)-1].value != "{" {
		return javaToken{}, false
	}
	cursor := 0
	for cursor < len(tokens)-1 {
		if tokens[cursor].value == "@" {
			cursor++
			if cursor >= len(tokens)-1 || !javaTokenIsSourceName(tokens[cursor]) {
				return javaToken{}, false
			}
			cursor++
			for cursor+1 < len(tokens)-1 && tokens[cursor].value == "." &&
				javaTokenIsSourceName(tokens[cursor+1]) {
				cursor += 2
			}
			if cursor < len(tokens)-1 && tokens[cursor].value == "(" {
				depth := 0
				for cursor < len(tokens)-1 {
					switch tokens[cursor].value {
					case "(":
						depth++
					case ")":
						depth--
					}
					cursor++
					if depth == 0 {
						break
					}
				}
			}
			continue
		}
		switch tokens[cursor].value {
		case "public", "protected", "private":
			cursor++
			continue
		}
		break
	}
	if cursor < len(tokens)-1 && tokens[cursor].value == "<" {
		depth := 0
		for cursor < len(tokens)-1 {
			switch tokens[cursor].value {
			case "<":
				depth++
			case ">":
				depth--
			case ">>":
				depth -= 2
			case ">>>":
				depth -= 3
			}
			cursor++
			if depth <= 0 {
				break
			}
		}
	}
	if cursor+1 >= len(tokens) || tokens[cursor].value != typeName ||
		!javaTokenIsSourceName(tokens[cursor]) ||
		(tokens[cursor+1].value != "(" && tokens[cursor+1].value != "{") {
		return javaToken{}, false
	}
	return tokens[cursor], true
}

func javaStreamedArrayInitializerHeader(tokens []javaToken) bool {
	if len(tokens) < 2 || tokens[len(tokens)-1].value != "{" {
		return false
	}
	previous := tokens[len(tokens)-2].value
	if previous == "=" || previous == "default" {
		return true
	}
	if previous != "]" {
		return false
	}
	assignment := -1
	allocation := false
	for index := len(tokens) - 2; index >= 0; index-- {
		switch tokens[index].value {
		case "->":
			return false
		case "new":
			allocation = true
			index = -1
		case "=", "default":
			assignment = index
			index = -1
		}
	}
	return assignment >= 0 || allocation
}

func javaStreamedInitializerBodyHeader(tokens []javaToken, enumConstant bool) bool {
	if enumConstant || len(tokens) < 2 || tokens[len(tokens)-1].value != "{" {
		return false
	}
	if javaStreamedAnonymousTypeHeader(tokens, false) {
		return true
	}
	for index := len(tokens) - 2; index >= 0; index-- {
		switch tokens[index].value {
		case "->":
			return true
		case "switch":
			for cursor := index - 1; cursor >= 0; cursor-- {
				if tokens[cursor].value == "=" || tokens[cursor].value == "default" ||
					tokens[cursor].value == "->" {
					return true
				}
			}
			return false
		case ";", "}":
			return false
		}
	}
	return false
}

func javaStreamedBraceScopeAnchor(
	tokens []javaToken,
	kind string,
	switchRule bool,
) int {
	end := len(tokens) - 1
	if end < 0 {
		return -1
	}
	if kind == "anonymous" {
		for index := end - 1; index >= 0; index-- {
			if tokens[index].value == "new" {
				return index
			}
		}
	}
	if switchRule {
		for index := range end {
			if tokens[index].value == "case" || tokens[index].value == "default" {
				return index
			}
		}
	}
	for index := end - 1; index >= 0; index-- {
		switch tokens[index].value {
		case "switch":
			return index
		case "->":
			if index > 0 {
				return index - 1
			}
			return index
		}
	}
	return 0
}

func javaStreamedControlBraceKind(tokens []javaToken) string {
	if len(tokens) < 2 || tokens[len(tokens)-1].value != "{" {
		return ""
	}
	for _, token := range tokens[:len(tokens)-1] {
		switch token.value {
		case "if":
			return "if"
		case "else":
			return "else"
		case "try":
			return "try"
		case "catch":
			return "catch"
		case "finally":
			return "finally"
		case "do":
			return "do"
		}
	}
	return ""
}

func javaStreamedHeaderContains(tokens []javaToken, value string) bool {
	for _, token := range tokens {
		if token.value == value {
			return true
		}
	}
	return false
}

func javaStreamedStatementHeader(tokens []javaToken) bool {
	end := len(tokens)
	if end > 0 && (tokens[end-1].value == "{" || tokens[end-1].value == ";") {
		end--
	}
	cursor := 0
	for cursor+1 < end && javaTokenIsSourceName(tokens[cursor]) &&
		tokens[cursor+1].value == ":" {
		cursor += 2
	}
	if cursor >= end {
		return false
	}
	switch tokens[cursor].value {
	case "if", "while", "for", "synchronized", "do", "switch":
		return true
	default:
		return false
	}
}

func javaStreamedPendingStatementAccepts(tokens []javaToken, next string) bool {
	if len(tokens) == 0 {
		return false
	}
	switch next {
	case "else":
		for _, token := range tokens {
			if token.value == "if" {
				return true
			}
		}
	case "while":
		dos := 0
		whiles := 0
		for _, token := range tokens {
			switch token.value {
			case "do":
				dos++
			case "while":
				whiles++
			}
		}
		return dos > whiles
	}
	return false
}

func javaStreamedHeaderHasScopedInitializer(tokens []javaToken) bool {
	allocation := false
	argumentsClosed := false
	for _, token := range tokens {
		switch token.value {
		case "->", "switch":
			return true
		case "new":
			allocation = true
			argumentsClosed = false
		case ")":
			if allocation {
				argumentsClosed = true
			}
		case "{":
			if allocation && argumentsClosed {
				return true
			}
		}
	}
	return false
}

func javaStreamedAnonymousTypeHeader(
	tokens []javaToken,
	enumConstant bool,
) bool {
	if enumConstant {
		_, ok := javaStreamedEnumConstantName(tokens)
		return ok
	}
	end := len(tokens) - 1
	if end < 1 || tokens[end].value != "{" || tokens[end-1].value != ")" {
		return false
	}
	depth := 0
	open := -1
	for cursor := end - 1; cursor >= 0; cursor-- {
		switch tokens[cursor].value {
		case ")":
			depth++
		case "(":
			depth--
			if depth == 0 {
				open = cursor
				cursor = -1
			}
		}
	}
	if open < 0 {
		return false
	}
	nested := 0
	for cursor := open - 1; cursor >= 0; cursor-- {
		token := tokens[cursor]
		if nested > 0 {
			switch token.value {
			case ")":
				nested++
			case "(":
				nested--
			}
			continue
		}
		if token.value == "new" {
			return true
		}
		if token.value == ")" {
			nested = 1
			continue
		}
		if javaTokenIsSourceName(token) {
			continue
		}
		switch token.value {
		case ".", "@", "<", ">", ">>", ">>>", "?", "[", "]", ",",
			"extends", "super", "&":
			continue
		default:
			return false
		}
	}
	return false
}

func javaStreamedHeaderNeedsTerminator(tokens []javaToken) bool {
	for _, token := range tokens {
		switch token.value {
		case "=", "->":
			return true
		}
	}
	return false
}

func javaStreamedHeaderMayDeclare(
	tokens []javaToken,
	declarationHeader bool,
) bool {
	if declarationHeader {
		return true
	}
	sourceNames := 0
	typeKeyword := false
	parameterList := false
	for _, token := range tokens {
		if javaTokenIsSourceName(token) {
			sourceNames++
		}
		if javaPrimitiveOrVoid(token.value) {
			typeKeyword = true
		}
		if token.value == "(" {
			parameterList = true
		}
	}
	return sourceNames >= 2 || sourceNames >= 1 && (typeKeyword || parameterList)
}

func javaStreamedSimpleFieldName(tokens []javaToken) (javaToken, bool) {
	end := len(tokens)
	if end == 0 || tokens[end-1].value != ";" {
		return javaToken{}, false
	}
	end--
	start := 0
	for start < end {
		switch tokens[start].value {
		case "public", "protected", "private", "static", "final", "transient",
			"volatile":
			start++
		default:
			goto prefixComplete
		}
	}

prefixComplete:
	if end-start != 2 || !javaTokenIsSourceName(tokens[start+1]) ||
		!javaTokenIsSourceName(tokens[start]) &&
			(!javaPrimitiveOrVoid(tokens[start].value) || tokens[start].value == "void") {
		return javaToken{}, false
	}
	return tokens[start+1], true
}

func javaStreamedEnumConstantName(tokens []javaToken) (javaToken, bool) {
	end := len(tokens)
	if end > 0 {
		switch tokens[end-1].value {
		case "{", ",", ";":
			end--
		}
	}
	cursor := 0
	for cursor < end && tokens[cursor].value == "@" {
		cursor++
		if cursor >= end || !javaTokenIsSourceName(tokens[cursor]) {
			return javaToken{}, false
		}
		cursor++
		for cursor+1 < end && tokens[cursor].value == "." &&
			javaTokenIsSourceName(tokens[cursor+1]) {
			cursor += 2
		}
		if cursor < end && tokens[cursor].value == "(" {
			depth := 0
			for cursor < end {
				switch tokens[cursor].value {
				case "(":
					depth++
				case ")":
					depth--
				}
				cursor++
				if depth == 0 {
					break
				}
			}
		}
	}
	if cursor >= end || !javaTokenIsSourceName(tokens[cursor]) {
		return javaToken{}, false
	}
	return tokens[cursor], true
}

func javaStreamedImportHeader(tokens []javaToken, moduleDirective bool) bool {
	if len(tokens) < 3 || tokens[len(tokens)-1].value != ";" {
		return false
	}
	cursor := 0
	singleModuleImport := false
	if moduleDirective {
		if tokens[cursor].value != "requires" {
			return false
		}
		cursor++
		for cursor < len(tokens)-1 &&
			(tokens[cursor].value == "static" || tokens[cursor].value == "transitive") {
			cursor++
		}
	} else {
		if tokens[cursor].value != "import" {
			return false
		}
		cursor++
		if cursor < len(tokens)-1 && tokens[cursor].value == "module" {
			singleModuleImport = true
			cursor++
		} else if cursor < len(tokens)-1 && tokens[cursor].value == "static" {
			cursor++
		}
	}
	if cursor >= len(tokens)-1 || !javaTokenIsSourceName(tokens[cursor]) {
		return false
	}
	cursor++
	for cursor+1 < len(tokens) && tokens[cursor].value == "." {
		if tokens[cursor+1].value == "*" && cursor+2 == len(tokens)-1 {
			if moduleDirective || singleModuleImport {
				return false
			}
			cursor += 2
			break
		}
		if !javaTokenIsSourceName(tokens[cursor+1]) {
			return false
		}
		cursor += 2
	}
	return cursor == len(tokens)-1
}
