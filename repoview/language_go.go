package repoview

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

type goLanguage struct {
	languageDefinition
}

func newGoLanguage() goLanguage {
	identifier := `[\p{L}_][\p{L}\p{Nd}_]*`
	return goLanguage{newLanguageDefinition(
		"go",
		[]string{
			`^func\s+(?:\([^)]*\)\s*)?(` + identifier + `)`,
			`^type\s+(` + identifier + `)`,
			`^(?:const|var)\s+(` + identifier + `)`,
		},
		goScope,
		goImports,
		commentStyleCLike,
		false,
	)}
}

func (g goLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	definitions, ok := parsedGoDefinitions(lines)
	if !ok {
		return g.languageDefinition.sourceDefinitions(goSearchLines(lines, true, true))
	}
	return definitions
}

func parsedGoDefinitions(lines []string) ([]sourceDefinition, bool) {
	fileSet, file, parseErr := parseGoSource(lines, parser.SkipObjectResolution)
	if file == nil {
		return nil, false
	}
	definitions := make([]sourceDefinition, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.FuncDecl:
			if declaration.Name.Name == "_" {
				return true
			}
			definitions = append(definitions, goDefinition(
				fileSet,
				declaration.Name.Name,
				declaration.Name.Pos(),
				declaration.Pos(),
				declaration.End(),
			))
		case *ast.GenDecl:
			definitions = append(definitions, goGeneralDefinitions(fileSet, declaration)...)
		case *ast.InterfaceType:
			definitions = append(definitions, goInterfaceDefinitions(fileSet, declaration)...)
		default:
		}
		return true
	})
	if parseErr != nil {
		fallback := newGoLanguage().languageDefinition.sourceDefinitions(
			goSearchLines(lines, true, true),
		)
		for _, definition := range fallback {
			if definitionCount(definitions, definition.line, definition.symbol) == 0 {
				definitions = append(definitions, definition)
			}
		}
	}
	sort.SliceStable(definitions, func(first, second int) bool {
		if definitions[first].line != definitions[second].line {
			return definitions[first].line < definitions[second].line
		}
		return definitions[first].column < definitions[second].column
	})
	return definitions, true
}

func goGeneralDefinitions(fileSet *token.FileSet, declaration *ast.GenDecl) []sourceDefinition {
	definitions := make([]sourceDefinition, 0)
	if declaration.Tok == token.TYPE {
		for _, specification := range declaration.Specs {
			typeSpecification, ok := specification.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeSpecification.Name.Name == "_" {
				continue
			}
			definitions = append(definitions, goDefinition(
				fileSet,
				typeSpecification.Name.Name,
				typeSpecification.Name.Pos(),
				typeSpecification.Pos(),
				typeSpecification.End(),
			))
		}
		return definitions
	}
	if declaration.Tok == token.CONST || declaration.Tok == token.VAR {
		for _, specification := range declaration.Specs {
			valueSpecification, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for nameIndex, name := range valueSpecification.Names {
				if name.Name == "_" {
					continue
				}
				position := goPhysicalPosition(fileSet, name.Pos())
				definition := sourceDefinition{
					symbol:     name.Name,
					line:       position.Line,
					column:     position.Column,
					scopeStart: position.Line,
					scopeEnd:   position.Line,
					ownsScope:  false,
				}
				if start, end, ok := goValueScope(fileSet, valueSpecification, nameIndex); ok {
					definition.scopeStart = min(position.Line, start)
					definition.scopeEnd = end
					definition.ownsScope = true
				}
				definitions = append(definitions, definition)
			}
		}
	}
	return definitions
}

func goInterfaceDefinitions(
	fileSet *token.FileSet,
	interfaceType *ast.InterfaceType,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0)
	for _, field := range interfaceType.Methods.List {
		if _, ok := field.Type.(*ast.FuncType); !ok {
			continue
		}
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			definitions = append(definitions, goDefinition(
				fileSet,
				name.Name,
				name.Pos(),
				field.Pos(),
				field.End(),
			))
		}
	}
	return definitions
}

func goValueScope(
	fileSet *token.FileSet,
	specification *ast.ValueSpec,
	nameIndex int,
) (int, int, bool) {
	expressions := make([]ast.Expr, 0, 2)
	switch {
	case len(specification.Values) == len(specification.Names):
		expressions = append(expressions, specification.Values[nameIndex])
	case len(specification.Values) == 1:
		expressions = append(expressions, specification.Values[0])
	}
	for _, expression := range expressions {
		start, end, ok := goScopedExpression(expression)
		if ok {
			return goPhysicalLine(fileSet, start), goPhysicalLine(fileSet, end), true
		}
	}
	if specification.Type != nil {
		start, end, ok := goScopedTypeExpression(specification.Type)
		if ok {
			return goPhysicalLine(fileSet, start), goPhysicalLine(fileSet, end), true
		}
	}
	return 0, 0, false
}

func goScopedTypeExpression(expression ast.Expr) (token.Pos, token.Pos, bool) {
	if goExpressionContainsScope(expression, false) {
		return expression.Pos(), expression.End(), true
	}
	return token.NoPos, token.NoPos, false
}

func goScopedExpression(expression ast.Expr) (token.Pos, token.Pos, bool) {
	if goExpressionContainsScope(expression, true) {
		return expression.Pos(), expression.End(), true
	}
	return token.NoPos, token.NoPos, false
}

func goExpressionContainsScope(expression ast.Expr, includeValues bool) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.StructType, *ast.InterfaceType:
			found = true
			return false
		case *ast.CompositeLit, *ast.FuncLit:
			if includeValues {
				found = true
				return false
			}
			return true
		default:
			return !found
		}
	})
	return found
}

func goDefinition(
	fileSet *token.FileSet,
	symbol string,
	position, start, end token.Pos,
) sourceDefinition {
	return sourceDefinition{
		symbol:     symbol,
		line:       goPhysicalPosition(fileSet, position).Line,
		column:     goPhysicalPosition(fileSet, position).Column,
		scopeStart: goPhysicalLine(fileSet, start),
		scopeEnd:   goPhysicalLine(fileSet, end),
		ownsScope:  true,
	}
}

func goScope(lines []string, lineNo int) (int, int) {
	if definitions, ok := parsedGoDefinitions(lines); ok {
		bestStart, bestEnd := 0, 0
		for _, definition := range definitions {
			if !definition.ownsScope {
				continue
			}
			if lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
				continue
			}
			if bestStart == 0 || definition.scopeEnd-definition.scopeStart < bestEnd-bestStart {
				bestStart = definition.scopeStart
				bestEnd = definition.scopeEnd
			}
		}
		if bestStart > 0 {
			return bestStart, bestEnd
		}
	}
	return braceScopeResolver(goSearchLines(lines, true, true), lineNo)
}

func goImports(lines []string) (int, int, bool) {
	fileSet, file, parseErr := parseGoSource(
		lines,
		parser.ParseComments|parser.SkipObjectResolution,
	)
	start, end := 0, 0
	if file != nil {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.IMPORT {
				continue
			}
			declarationStart := goPhysicalLine(fileSet, general.Pos())
			if general.Doc != nil {
				declarationStart = goPhysicalLine(fileSet, general.Doc.Pos())
			}
			if start == 0 || declarationStart < start {
				start = declarationStart
			}
			declarationEnd := goPhysicalLine(fileSet, general.End())
			if declarationEnd > end {
				end = declarationEnd
			}
		}
		if parseErr == nil {
			return start, end, start > 0 && end >= start
		}
	}
	fallbackStart, fallbackEnd, fallbackOK := fallbackGoImports(lines)
	if fallbackOK {
		if start == 0 || fallbackStart < start {
			start = fallbackStart
		}
		if fallbackEnd > end {
			end = fallbackEnd
		}
	}
	return start, end, start > 0 && end >= start
}

func fallbackGoImports(lines []string) (int, int, bool) {
	const (
		goImportOutside = iota
		goImportKeyword
		goImportSingle
		goImportGroup
	)

	source := strings.Join(lines, "\n")
	content := []byte(source)
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("source.go", fileSet.Base(), len(content))
	var lexer scanner.Scanner
	lexer.Init(file, content, func(_ token.Position, _ string) {}, scanner.ScanComments)

	start, end := 0, 0
	state := goImportOutside
	groupDepth := 0
	currentStart, currentEnd := 0, 0
	commentStart, commentEnd := 0, 0
	record := func() {
		if start == 0 || currentStart < start {
			start = currentStart
		}
		if currentEnd > end {
			end = currentEnd
		}
		currentStart, currentEnd = 0, 0
	}

	for {
		position, scannedToken, literal := lexer.Scan()
		line := goPhysicalLine(fileSet, position)
		tokenEndLine := line + strings.Count(literal, "\n")

		if scannedToken == token.COMMENT {
			if state == goImportOutside {
				offset := file.Offset(position)
				lineStart := strings.LastIndexByte(source[:offset], '\n') + 1
				standalone := strings.TrimSpace(source[lineStart:offset]) == ""
				if !standalone {
					commentStart, commentEnd = 0, 0
					continue
				}
				if commentStart == 0 || line > commentEnd+1 {
					commentStart = line
				}
				commentEnd = tokenEndLine
			} else {
				currentEnd = max(currentEnd, tokenEndLine)
			}
			continue
		}

		switch state {
		case goImportOutside:
			if scannedToken == token.IMPORT {
				currentStart, currentEnd = line, line
				if commentStart > 0 && line <= commentEnd+1 {
					currentStart = commentStart
				}
				commentStart, commentEnd = 0, 0
				state = goImportKeyword
			} else if scannedToken != token.SEMICOLON || literal == ";" {
				commentStart, commentEnd = 0, 0
			}
		case goImportKeyword:
			switch scannedToken { //nolint:exhaustive // Other tokens begin a single import specification.
			case token.LPAREN:
				groupDepth = 1
				currentEnd = max(currentEnd, tokenEndLine)
				state = goImportGroup
			case token.SEMICOLON:
				record()
				state = goImportOutside
			case token.EOF:
				record()
				return start, end, start > 0 && end >= start
			default:
				currentEnd = max(currentEnd, tokenEndLine)
				state = goImportSingle
			}
		case goImportSingle:
			switch scannedToken { //nolint:exhaustive // Only a terminator changes the single-import state.
			case token.SEMICOLON:
				record()
				state = goImportOutside
			case token.EOF:
				record()
				return start, end, start > 0 && end >= start
			default:
				currentEnd = max(currentEnd, tokenEndLine)
			}
		case goImportGroup:
			switch scannedToken { //nolint:exhaustive // Only parentheses affect grouped-import depth.
			case token.LPAREN:
				groupDepth++
			case token.RPAREN:
				groupDepth--
			}
			if scannedToken == token.EOF {
				currentEnd = len(lines)
				record()
				return start, end, start > 0 && end >= start
			}
			currentEnd = max(currentEnd, tokenEndLine)
			if groupDepth == 0 {
				record()
				state = goImportOutside
			}
		}

		if scannedToken == token.EOF {
			return start, end, start > 0 && end >= start
		}
	}
}

func (goLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	cleaned := strings.Split(maskGoSource(source, true, false), "\n")
	original := strings.Split(source, "\n")
	rawStringLines := goRawStringLines(source)
	for idx := range cleaned {
		if cleaned[idx] != original[idx] && !rawStringLines[idx+1] {
			cleaned[idx] = strings.TrimRight(cleaned[idx], " \t")
		}
	}
	return strings.Join(cleaned, "\n")
}

func (goLanguage) ignoredSearchLines([]string, bool, bool) map[int]bool {
	return map[int]bool{}
}

func (goLanguage) searchLines(lines []string, noComments, noStrings bool) []string {
	return goSearchLines(lines, noComments, noStrings)
}

func (g goLanguage) cleanSourceLines(
	lines []string,
	dropComments, dropDocstrings bool,
) []string {
	return strings.Split(
		g.cleanSource(strings.Join(lines, "\n"), dropComments, dropDocstrings),
		"\n",
	)
}

func (goLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	fileSet, file, parseErr := parseGoSource(lines, parser.SkipObjectResolution)
	if file == nil {
		return "", false
	}
	symbol := ""
	ast.Inspect(file, func(node ast.Node) bool {
		if symbol != "" {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		called := goCalledIdentifier(call.Fun)
		if called == nil || goPhysicalLine(fileSet, called.Pos()) != lineNo {
			return true
		}
		symbol = called.Name
		return false
	})
	if symbol == "" {
		ast.Inspect(file, func(node ast.Node) bool {
			if symbol != "" {
				return false
			}
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || goPhysicalLine(fileSet, selector.Sel.Pos()) != lineNo {
				return true
			}
			symbol = selector.Sel.Name
			return false
		})
	}
	if symbol == "" {
		ast.Inspect(file, func(node ast.Node) bool {
			if symbol != "" {
				return false
			}
			identifier, ok := node.(*ast.Ident)
			if !ok || identifier.Name == "_" ||
				goPhysicalLine(fileSet, identifier.Pos()) != lineNo {
				return true
			}
			symbol = identifier.Name
			return false
		})
	}
	return symbol, symbol != "" || parseErr == nil
}

func goCalledIdentifier(expression ast.Expr) *ast.Ident {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression
	case *ast.SelectorExpr:
		return expression.Sel
	case *ast.IndexExpr:
		return goCalledIdentifier(expression.X)
	case *ast.IndexListExpr:
		return goCalledIdentifier(expression.X)
	case *ast.ParenExpr:
		return goCalledIdentifier(expression.X)
	case *ast.FuncLit:
		return nil
	default:
		return nil
	}
}

func (goLanguage) stripComment(line string) string {
	return strings.TrimRight(maskGoSource(line, true, false), " \t")
}

func goSearchLines(lines []string, noComments, noStrings bool) []string {
	return strings.Split(maskGoSource(strings.Join(lines, "\n"), noComments, noStrings), "\n")
}

func maskGoSource(source string, comments, stringsAndRunes bool) string {
	content := []byte(source)
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("source.go", fileSet.Base(), len(content))
	var lexer scanner.Scanner
	lexer.Init(file, content, func(_ token.Position, _ string) {}, scanner.ScanComments)
	for {
		position, scannedToken, literal := lexer.Scan()
		if scannedToken == token.EOF {
			break
		}
		mask := comments && scannedToken == token.COMMENT
		mask = mask || stringsAndRunes && (scannedToken == token.STRING || scannedToken == token.CHAR)
		if !mask {
			continue
		}
		start := file.Offset(position)
		end := goOriginalTokenEnd(content, start, literal)
		for idx := start; idx < end; idx++ {
			if content[idx] != '\n' && content[idx] != '\r' {
				content[idx] = ' '
			}
		}
	}
	return string(content)
}

func goRawStringLines(source string) map[int]bool {
	content := []byte(source)
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("source.go", fileSet.Base(), len(content))
	var lexer scanner.Scanner
	lexer.Init(file, content, func(_ token.Position, _ string) {}, scanner.ScanComments)
	rawLines := map[int]bool{}
	for {
		position, scannedToken, literal := lexer.Scan()
		if scannedToken == token.EOF {
			return rawLines
		}
		if scannedToken != token.STRING || !strings.HasPrefix(literal, "`") {
			continue
		}
		start := goPhysicalLine(fileSet, position)
		end := start + strings.Count(literal, "\n")
		for line := start; line <= end; line++ {
			rawLines[line] = true
		}
	}
}

func goOriginalTokenEnd(content []byte, start int, literal string) int {
	end := start
	literalOffset := 0
	for end < len(content) && literalOffset < len(literal) {
		if content[end] == literal[literalOffset] {
			end++
			literalOffset++
			continue
		}
		if content[end] != '\r' {
			literalOffset++
		}
		end++
	}
	return end
}

func parseGoSource(lines []string, mode parser.Mode) (*token.FileSet, *ast.File, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(
		fileSet,
		"source.go",
		strings.Join(lines, "\n"),
		mode|parser.AllErrors,
	)
	return fileSet, file, err
}

func goPhysicalLine(fileSet *token.FileSet, position token.Pos) int {
	return goPhysicalPosition(fileSet, position).Line
}

func goPhysicalPosition(fileSet *token.FileSet, position token.Pos) token.Position {
	return fileSet.PositionFor(position, false)
}
