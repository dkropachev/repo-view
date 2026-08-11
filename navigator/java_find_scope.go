package navigator

import (
	"sort"
	"strings"
)

// javaPreparedFindScopeResolver is an immutable per-file index for Find. It
// keeps the Java analysis's owned definition slice internal and resolves every
// hit without revalidating line snapshots, cloning definitions, or scanning
// all definitions and scopes again.
type javaPreparedFindScopeResolver struct {
	sourceDefinitions      []sourceDefinition
	definitionColumnsByKey map[javaFindDefinitionKey][]int
	definitionCounts       map[javaFindDefinitionKey]int
	positionOwners         sourceLineDefinitionOwners
	positionBoundaries     sourceDefinitionPositionBoundaries
	positionFallbackByLine []int
	ownerByLine            []int
	enclosingByLine        []javaLineScope
	commentOnlyByLine      []bool
	ownerAtDefinition      map[int]int
	ownerDefinitionLines   []int
	substantiveLines       []int
	lineCount              int
}

type javaFindDefinitionKey struct {
	symbol string
	line   int
}

type javaFindScopeCandidate struct {
	start           int
	end             int
	definitionIndex int
	size            int
	declarationLine int
}

func (j javaLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return newJavaPreparedFindScopeResolver(
		lines, analysis.definitions, analysis.scopes,
	)
}

func newJavaPreparedFindScopeResolver(
	lines []string,
	definitions []sourceDefinition,
	scopes []javaLineScope,
) *javaPreparedFindScopeResolver {
	lineCount := len(lines)
	resolver := &javaPreparedFindScopeResolver{
		sourceDefinitions:      definitions,
		definitionColumnsByKey: make(map[javaFindDefinitionKey][]int, len(definitions)),
		definitionCounts:       make(map[javaFindDefinitionKey]int, len(definitions)),
		positionOwners:         newSourceLineDefinitionOwners(definitions),
		positionBoundaries:     newSourceDefinitionPositionBoundaries(definitions),
		positionFallbackByLine: make([]int, lineCount+1),
		ownerByLine:            make([]int, lineCount+1),
		enclosingByLine:        make([]javaLineScope, lineCount+1),
		commentOnlyByLine:      make([]bool, lineCount+1),
		ownerAtDefinition:      make(map[int]int, len(definitions)),
		ownerDefinitionLines:   make([]int, 0),
		substantiveLines:       make([]int, 0, lineCount),
		lineCount:              lineCount,
	}
	for _, definition := range definitions {
		key := javaFindDefinitionKey{
			symbol: definition.symbol,
			line:   definition.line,
		}
		resolver.definitionCounts[key]++
		if definition.column > 0 {
			resolver.definitionColumnsByKey[key] = append(
				resolver.definitionColumnsByKey[key], definition.column,
			)
		}
	}
	resolver.indexNamedScopes()
	resolver.indexPositionFallbacks()
	resolver.indexEnclosingScopes(scopes)
	resolver.indexFallbackLines(lines)
	return resolver
}

func (r *javaPreparedFindScopeResolver) navigationScopeAt(
	lineNo, column int,
	structuralLine string,
) (int, int) {
	if r != nil && (len(r.positionOwners[lineNo]) > 0 ||
		r.positionBoundaries.hasLine(lineNo)) {
		owner := 0
		if len(r.positionOwners[lineNo]) > 0 {
			owner = r.positionOwners.ownerAt(
				r.sourceDefinitions, lineNo, column, structuralLine,
			)
		}
		if owner == 0 && lineNo >= 1 && lineNo < len(r.positionFallbackByLine) {
			owner = r.positionFallbackByLine[lineNo]
		}
		owner = r.positionBoundaries.ownerAtColumn(
			r.sourceDefinitions, owner, lineNo, column,
		)
		if owner > 0 {
			definition := r.sourceDefinitions[owner-1]
			return definition.scopeStart, definition.scopeEnd
		}
		return lineNo, lineNo
	}
	return r.navigationScope(lineNo)
}

func (r *javaPreparedFindScopeResolver) definitionCount(
	lineNo int,
	symbol string,
) int {
	if r == nil || lineNo < 1 || lineNo > r.lineCount || symbol == "" {
		return 0
	}
	return r.definitionCounts[javaFindDefinitionKey{symbol: symbol, line: lineNo}]
}

func (r *javaPreparedFindScopeResolver) definitionColumns(
	lineNo int,
	symbol string,
) []int {
	if r == nil {
		return nil
	}
	return r.definitionColumnsByKey[javaFindDefinitionKey{
		line: lineNo, symbol: symbol,
	}]
}

func (r *javaPreparedFindScopeResolver) navigationScope(lineNo int) (int, int) {
	if r == nil || lineNo < 1 || lineNo > r.lineCount {
		return lineNo, lineNo
	}
	if owner := r.ownerByLine[lineNo]; owner > 0 {
		definition := r.sourceDefinitions[owner-1]
		return definition.scopeStart, definition.scopeEnd
	}
	if scope := r.enclosingByLine[lineNo]; scope.start > 0 {
		return scope.start, scope.end
	}
	return lineNo, lineNo
}

func (r *javaPreparedFindScopeResolver) scopeName(lineNo int) string {
	if r == nil || lineNo < 1 || lineNo > r.lineCount {
		return ""
	}
	if owner := r.ownerByLine[lineNo]; owner > 0 {
		return r.sourceDefinitions[owner-1].symbol
	}

	start, end := r.navigationScope(lineNo)
	backwardEnd := min(lineNo, end)
	ownerLineIndex := sort.Search(len(r.ownerDefinitionLines), func(index int) bool {
		return r.ownerDefinitionLines[index] > backwardEnd
	}) - 1
	if ownerLineIndex >= 0 {
		ownerLine := r.ownerDefinitionLines[ownerLineIndex]
		if ownerLine >= start {
			return r.sourceDefinitions[r.ownerAtDefinition[ownerLine]-1].symbol
		}
	}

	forwardEnd := end
	if start == lineNo && end == lineNo &&
		r.commentOnlyByLine[lineNo] {
		forwardEnd = r.lineCount
	}
	nextIndex := sort.SearchInts(r.substantiveLines, lineNo+1)
	if nextIndex >= len(r.substantiveLines) ||
		r.substantiveLines[nextIndex] > forwardEnd {
		return ""
	}
	if owner := r.ownerAtDefinition[r.substantiveLines[nextIndex]]; owner > 0 {
		return r.sourceDefinitions[owner-1].symbol
	}
	return ""
}

func (r *javaPreparedFindScopeResolver) scopeNameAt(
	lineNo, column int,
	structuralLine string,
) string {
	if r != nil && (len(r.positionOwners[lineNo]) > 0 ||
		r.positionBoundaries.hasLine(lineNo)) {
		owner := 0
		if len(r.positionOwners[lineNo]) > 0 {
			owner = r.positionOwners.ownerAt(
				r.sourceDefinitions, lineNo, column, structuralLine,
			)
		}
		if owner == 0 && lineNo >= 1 && lineNo < len(r.positionFallbackByLine) {
			owner = r.positionFallbackByLine[lineNo]
		}
		owner = r.positionBoundaries.ownerAtColumn(
			r.sourceDefinitions, owner, lineNo, column,
		)
		if owner > 0 {
			return r.sourceDefinitions[owner-1].symbol
		}
		return ""
	}
	return r.scopeName(lineNo)
}

func (r *javaPreparedFindScopeResolver) indexNamedScopes() {
	before := make([]javaFindScopeCandidate, 0, len(r.sourceDefinitions))
	after := make([]javaFindScopeCandidate, 0, len(r.sourceDefinitions))
	for index, definition := range r.sourceDefinitions {
		if !definition.ownsScope || definition.scopeStart > definition.scopeEnd {
			continue
		}
		start := max(1, definition.scopeStart)
		end := min(r.lineCount, definition.scopeEnd)
		if start > end {
			continue
		}
		if definition.line >= 1 && definition.line <= r.lineCount {
			if _, exists := r.ownerAtDefinition[definition.line]; !exists {
				r.ownerAtDefinition[definition.line] = index + 1
				r.ownerDefinitionLines = append(
					r.ownerDefinitionLines, definition.line,
				)
			}
		}
		size := definition.scopeEnd - definition.scopeStart
		if beforeStart := max(start, definition.line); beforeStart <= end {
			before = append(before, javaFindScopeCandidate{
				start: beforeStart, end: end, definitionIndex: index,
				size: size, declarationLine: definition.line,
			})
		}
		if afterEnd := min(end, definition.line-1); start <= afterEnd {
			after = append(after, javaFindScopeCandidate{
				start: start, end: afterEnd, definitionIndex: index,
				size: size, declarationLine: definition.line,
			})
		}
	}
	sort.Ints(r.ownerDefinitionLines)

	sort.Slice(before, func(first, second int) bool {
		return javaFindScopeCandidateLess(before[first], before[second], true)
	})
	sort.Slice(after, func(first, second int) bool {
		return javaFindScopeCandidateLess(after[first], after[second], false)
	})
	next := javaFindScopeUnassignedLines(r.lineCount)
	paintJavaFindScopeCandidates(r.ownerByLine, before, next)
	paintJavaFindScopeCandidates(r.ownerByLine, after, next)
}

func (r *javaPreparedFindScopeResolver) indexPositionFallbacks() {
	before := make([]javaFindScopeCandidate, 0, len(r.sourceDefinitions))
	after := make([]javaFindScopeCandidate, 0, len(r.sourceDefinitions))
	for index, definition := range r.sourceDefinitions {
		if !definition.ownsScope || definition.scopeStart > definition.scopeEnd {
			continue
		}
		start := max(1, definition.scopeStart)
		end := min(r.lineCount, definition.scopeEnd)
		if start > end {
			continue
		}
		size := end - start
		if beforeStart := max(start, definition.line+1); beforeStart <= end {
			before = append(before, javaFindScopeCandidate{
				start: beforeStart, end: end, definitionIndex: index,
				size: size, declarationLine: definition.line,
			})
		}
		if afterEnd := min(end, definition.line-1); start <= afterEnd {
			after = append(after, javaFindScopeCandidate{
				start: start, end: afterEnd, definitionIndex: index,
				size: size, declarationLine: definition.line,
			})
		}
	}
	sort.Slice(before, func(first, second int) bool {
		return javaFindScopeCandidateLess(before[first], before[second], true)
	})
	sort.Slice(after, func(first, second int) bool {
		return javaFindScopeCandidateLess(after[first], after[second], false)
	})
	next := javaFindScopeUnassignedLines(r.lineCount)
	paintJavaFindScopeCandidates(r.positionFallbackByLine, before, next)
	paintJavaFindScopeCandidates(r.positionFallbackByLine, after, next)
}

func (r *javaPreparedFindScopeResolver) indexEnclosingScopes(
	scopes []javaLineScope,
) {
	candidates := make([]javaFindScopeCandidate, 0, len(scopes))
	for index, scope := range scopes {
		start := max(1, scope.start)
		end := min(r.lineCount, scope.end)
		if start > end {
			continue
		}
		candidates = append(candidates, javaFindScopeCandidate{
			start: start, end: end, definitionIndex: index,
			size: scope.end - scope.start, declarationLine: scope.start,
		})
	}
	sort.Slice(candidates, func(first, second int) bool {
		left, right := candidates[first], candidates[second]
		if left.size != right.size {
			return left.size < right.size
		}
		if left.start != right.start {
			return left.start > right.start
		}
		return left.definitionIndex < right.definitionIndex
	})
	best := make([]int, r.lineCount+1)
	paintJavaFindScopeCandidates(
		best, candidates, javaFindScopeUnassignedLines(r.lineCount),
	)
	for lineNo := 1; lineNo <= r.lineCount; lineNo++ {
		if best[lineNo] > 0 {
			r.enclosingByLine[lineNo] = scopes[best[lineNo]-1]
		}
	}
}

func (r *javaPreparedFindScopeResolver) indexFallbackLines(lines []string) {
	for index, line := range lines {
		r.commentOnlyByLine[index+1] = javaFindScopeCommentLine(line)
		if !javaFindScopeSkipsLine(line) {
			r.substantiveLines = append(r.substantiveLines, index+1)
		}
	}
}

func javaFindScopeCandidateLess(
	first, second javaFindScopeCandidate,
	before bool,
) bool {
	if first.size != second.size {
		return first.size < second.size
	}
	if first.declarationLine != second.declarationLine {
		if before {
			return first.declarationLine > second.declarationLine
		}
		return first.declarationLine < second.declarationLine
	}
	return first.definitionIndex < second.definitionIndex
}

func javaFindScopeUnassignedLines(lineCount int) []int {
	next := make([]int, lineCount+2)
	for index := range next {
		next[index] = index
	}
	return next
}

func paintJavaFindScopeCandidates(
	destination []int,
	candidates []javaFindScopeCandidate,
	next []int,
) {
	for _, candidate := range candidates {
		for lineNo := javaFindScopeNextLine(next, candidate.start); lineNo <= candidate.end; lineNo = javaFindScopeNextLine(next, lineNo) {
			destination[lineNo] = candidate.definitionIndex + 1
			next[lineNo] = javaFindScopeNextLine(next, lineNo+1)
		}
	}
}

func javaFindScopeNextLine(next []int, lineNo int) int {
	root := lineNo
	for next[root] != root {
		root = next[root]
	}
	for next[lineNo] != lineNo {
		parent := next[lineNo]
		next[lineNo] = root
		lineNo = parent
	}
	return root
}

func javaFindScopeSkipsLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || javaFindScopeCommentLine(trimmed)
}

func javaFindScopeCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "#")
}
