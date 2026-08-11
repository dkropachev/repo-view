package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const cMaximumIndexedFindLines = 256 << 10

// cPreparedFindScopeResolver is an immutable per-file index used by Find and
// navigation. Moderate files use direct line indexes. Larger files use a
// run-length winner index, keeping lookups logarithmic without allocating in
// proportion to an attacker-controlled line count.
type cPreparedFindScopeResolver struct {
	definitionColumnsByKey map[cFindDefinitionKey][]int
	definitionCounts       map[cFindDefinitionKey]int
	definitions            []sourceDefinition
	positionOwners         sourceLineDefinitionOwners
	positionBoundaries     sourceDefinitionPositionBoundaries
	positionFallbackByLine []int
	positionFallbackRuns   []cFindScopeRun
	scopes                 []cLineScope
	ownerByLine            []int
	scopeByLine            []cLineScope
	ownerRuns              []cFindScopeRun
	scopeRuns              []cFindScopeRun
	lineCount              int
}

type cFindDefinitionKey struct {
	symbol string
	line   int
}

type cFindScopeCandidate struct {
	start, end      int
	definitionIndex int
	size            int
}

type cFindScopeRun struct {
	start, end int
	value      int
}

type cFindScopeEvent struct {
	position  int
	candidate int
	add       bool
}

func (c cLanguage) prepareFindScopeResolver(lines []string) preparedFindScopeResolver {
	analysis := c.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func newCPreparedFindScopeResolver(
	definitions []sourceDefinition,
	scopes []cLineScope,
	lineCount int,
) *cPreparedFindScopeResolver {
	resolver := &cPreparedFindScopeResolver{
		definitions:            definitions,
		definitionCounts:       make(map[cFindDefinitionKey]int, len(definitions)),
		definitionColumnsByKey: make(map[cFindDefinitionKey][]int, len(definitions)),
		positionOwners:         newSourceLineDefinitionOwners(definitions),
		positionBoundaries:     newSourceDefinitionPositionBoundaries(definitions),
		lineCount:              lineCount,
	}
	for _, definition := range definitions {
		key := cFindDefinitionKey{
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
	ownerCandidates := cDefinitionOwnerCandidates(definitions, lineCount)
	positionFallbackCandidates := cPositionFallbackCandidates(definitions, lineCount)
	scopeCandidates := cEnclosingScopeCandidates(scopes, lineCount)
	if lineCount <= cMaximumIndexedFindLines {
		resolver.ownerByLine = make([]int, lineCount+1)
		resolver.positionFallbackByLine = make([]int, lineCount+1)
		resolver.scopeByLine = make([]cLineScope, lineCount+1)
		resolver.indexDefinitionOwners(ownerCandidates)
		paintCFindScopeCandidates(
			resolver.positionFallbackByLine,
			positionFallbackCandidates,
			cFindScopeUnassignedLines(lineCount),
		)
		resolver.indexEnclosingScopes(scopes, scopeCandidates)
	} else {
		resolver.scopes = append([]cLineScope(nil), scopes...)
		resolver.ownerRuns = cBuildFindScopeRuns(ownerCandidates, lineCount)
		resolver.positionFallbackRuns = cBuildFindScopeRuns(
			positionFallbackCandidates, lineCount,
		)
		resolver.scopeRuns = cBuildFindScopeRuns(scopeCandidates, lineCount)
	}
	return resolver
}

type sourceLineDefinitionOwners map[int][]int

func newSourceLineDefinitionOwners(
	definitions []sourceDefinition,
) sourceLineDefinitionOwners {
	owners := make(sourceLineDefinitionOwners)
	for index, definition := range definitions {
		if !definition.ownsScope || definition.line < 1 || definition.column < 1 {
			continue
		}
		owners[definition.line] = append(owners[definition.line], index)
	}
	for lineNo := range owners {
		indices := owners[lineNo]
		sort.SliceStable(indices, func(first, second int) bool {
			left, right := definitions[indices[first]], definitions[indices[second]]
			if left.column != right.column {
				return left.column < right.column
			}
			leftSize := left.scopeEnd - left.scopeStart
			rightSize := right.scopeEnd - right.scopeStart
			return leftSize > rightSize
		})
	}
	return owners
}

type sourceDefinitionPositionBoundaries struct {
	endingDefinitions map[int][]int
	parents           []int
}

func newSourceDefinitionPositionBoundaries(
	definitions []sourceDefinition,
) sourceDefinitionPositionBoundaries {
	boundaries := sourceDefinitionPositionBoundaries{
		endingDefinitions: make(map[int][]int),
		parents:           make([]int, len(definitions)),
	}
	owned := make([]int, 0, len(definitions))
	for index, definition := range definitions {
		if !definition.ownsScope || definition.scopeStart < 1 ||
			definition.scopeEnd < definition.scopeStart {
			continue
		}
		owned = append(owned, index)
		if definition.ownedEndColumn > 0 {
			boundaries.endingDefinitions[definition.scopeEnd] = append(
				boundaries.endingDefinitions[definition.scopeEnd], index,
			)
		}
	}
	for lineNo := range boundaries.endingDefinitions {
		indices := boundaries.endingDefinitions[lineNo]
		sort.SliceStable(indices, func(first, second int) bool {
			return sourceDefinitionPositionMoreSpecific(
				definitions[indices[first]], definitions[indices[second]],
			)
		})
	}
	sort.SliceStable(owned, func(first, second int) bool {
		left, right := definitions[owned[first]], definitions[owned[second]]
		if left.scopeStart != right.scopeStart {
			return left.scopeStart < right.scopeStart
		}
		if left.scopeEnd != right.scopeEnd {
			return left.scopeEnd > right.scopeEnd
		}
		if left.line != right.line {
			return left.line < right.line
		}
		return left.column < right.column
	})
	stack := make([]int, 0)
	for _, definitionIndex := range owned {
		definition := definitions[definitionIndex]
		for len(stack) > 0 {
			candidate := definitions[stack[len(stack)-1]]
			strictlyContains := sourceDefinitionPositionStrictlyContains(
				candidate, definition,
			)
			if strictlyContains {
				break
			}
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			boundaries.parents[definitionIndex] = stack[len(stack)-1] + 1
		}
		stack = append(stack, definitionIndex)
	}
	return boundaries
}

func (boundaries sourceDefinitionPositionBoundaries) hasLine(lineNo int) bool {
	_, ok := boundaries.endingDefinitions[lineNo]
	return ok
}

func (boundaries sourceDefinitionPositionBoundaries) ownerAtColumn(
	definitions []sourceDefinition,
	owner, lineNo, column int,
) int {
	if owner > 0 && owner <= len(definitions) &&
		!sourceDefinitionContainsPosition(definitions[owner-1], lineNo, column) {
		owner = boundaries.parent(owner)
	}
	for _, definitionIndex := range boundaries.endingDefinitions[lineNo] {
		candidate := definitions[definitionIndex]
		if !sourceDefinitionContainsPosition(candidate, lineNo, column) {
			continue
		}
		if owner == 0 || sourceDefinitionPositionMoreSpecific(
			candidate, definitions[owner-1],
		) {
			owner = definitionIndex + 1
		}
		break
	}
	for owner > 0 && owner <= len(definitions) {
		definitionIndex := owner - 1
		definition := definitions[definitionIndex]
		if sourceDefinitionContainsPosition(definition, lineNo, column) {
			return owner
		}
		owner = boundaries.parent(owner)
	}
	return 0
}

func (boundaries sourceDefinitionPositionBoundaries) parent(owner int) int {
	definitionIndex := owner - 1
	if definitionIndex < 0 || definitionIndex >= len(boundaries.parents) {
		return 0
	}
	return boundaries.parents[definitionIndex]
}

func sourceDefinitionContainsPosition(
	definition sourceDefinition,
	lineNo, column int,
) bool {
	if !definition.ownsScope || lineNo < definition.scopeStart ||
		lineNo > definition.scopeEnd || column < 1 {
		return false
	}
	if definition.line == lineNo && definition.column > column {
		return false
	}
	return definition.scopeEnd != lineNo || definition.ownedEndColumn <= 0 ||
		column < definition.ownedEndColumn
}

func sourceDefinitionPositionStrictlyContains(
	container, definition sourceDefinition,
) bool {
	if container.scopeStart > definition.scopeStart ||
		definition.scopeEnd > container.scopeEnd {
		return false
	}
	if container.scopeStart < definition.scopeStart ||
		definition.scopeEnd < container.scopeEnd {
		return true
	}
	startsEarlier := container.scopeStart < definition.scopeStart ||
		container.line < definition.line ||
		container.line == definition.line && container.column < definition.column
	endsLater := container.ownedEndColumn > 0 && definition.ownedEndColumn > 0 &&
		container.ownedEndColumn > definition.ownedEndColumn
	return startsEarlier && endsLater
}

func sourceDefinitionPositionMoreSpecific(
	candidate, current sourceDefinition,
) bool {
	candidateSize := candidate.scopeEnd - candidate.scopeStart
	currentSize := current.scopeEnd - current.scopeStart
	if candidateSize != currentSize {
		return candidateSize < currentSize
	}
	if candidate.scopeStart != current.scopeStart {
		return candidate.scopeStart > current.scopeStart
	}
	if candidate.line != current.line {
		return candidate.line > current.line
	}
	if candidate.column != current.column {
		return candidate.column > current.column
	}
	if candidate.ownedEndColumn != current.ownedEndColumn {
		if candidate.ownedEndColumn <= 0 {
			return false
		}
		if current.ownedEndColumn <= 0 {
			return true
		}
		return candidate.ownedEndColumn < current.ownedEndColumn
	}
	return false
}

func (owners sourceLineDefinitionOwners) ownerAt(
	definitions []sourceDefinition,
	lineNo, column int,
	structuralLine string,
) int {
	if column < 1 {
		return 0
	}
	indices := owners[lineNo]
	upper := sort.Search(len(indices), func(index int) bool {
		return definitions[indices[index]].column > column
	})
	for position := upper - 1; position >= 0; position-- {
		definitionIndex := indices[position]
		definition := definitions[definitionIndex]
		if definition.scopeStart > lineNo || lineNo > definition.scopeEnd {
			continue
		}
		if sourceDefinitionContainsColumn(
			definition, lineNo, column, structuralLine,
		) {
			return definitionIndex + 1
		}
	}
	return 0
}

func sourceDefinitionContainsColumn(
	definition sourceDefinition,
	lineNo, column int,
	structuralLine string,
) bool {
	if definition.column < 1 || column < definition.column {
		return false
	}
	if definition.scopeEnd > lineNo {
		return true
	}
	if lineNo == definition.scopeEnd && definition.ownedEndColumn > 0 {
		return column < definition.ownedEndColumn
	}
	if structuralLine == "" {
		return false
	}
	start := min(len(structuralLine), definition.column-1)
	openingLimit := len(structuralLine)
	for opening := start; opening < openingLimit; opening++ {
		if structuralLine[opening] != '{' {
			continue
		}
		closing := matchingBraceColumn(structuralLine, opening)
		hit := column - 1
		if hit <= opening || closing < 0 || hit < closing {
			return true
		}
		if closing >= openingLimit {
			break
		}
		opening = closing
	}
	return namedDefinitionContainsColumn(
		structuralLine, definition.symbol, start, column-1,
	)
}

func matchingBraceColumn(line string, opening int) int {
	depth := 0
	for index := opening; index < len(line); index++ {
		switch line[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func namedDefinitionContainsColumn(
	line, symbol string,
	start, hit int,
) bool {
	for offset := start; offset < len(line); {
		wordStart, wordEnd := nextIdentifierSpan(line, offset)
		offset = wordEnd
		if wordStart == offset || !strings.EqualFold(line[wordStart:offset], "END") {
			continue
		}
		nameStart, nameEnd := nextIdentifierSpan(line, offset)
		offset = nameEnd
		if strings.EqualFold(line[nameStart:offset], symbol) {
			return hit < wordStart
		}
	}
	return false
}

func nextIdentifierSpan(line string, offset int) (int, int) {
	for offset < len(line) {
		character, size := utf8.DecodeRuneInString(line[offset:])
		if isIdent(character) {
			break
		}
		offset += max(1, size)
	}
	start := offset
	for offset < len(line) {
		character, size := utf8.DecodeRuneInString(line[offset:])
		if !isIdent(character) {
			break
		}
		offset += max(1, size)
	}
	return start, offset
}

func (resolver *cPreparedFindScopeResolver) definitionCount(
	lineNo int,
	symbol string,
) int {
	if resolver == nil || lineNo < 1 || lineNo > resolver.lineCount || symbol == "" {
		return 0
	}
	return resolver.definitionCounts[cFindDefinitionKey{symbol: symbol, line: lineNo}]
}

func (resolver *cPreparedFindScopeResolver) definitionColumns(
	lineNo int,
	symbol string,
) []int {
	if resolver == nil {
		return nil
	}
	return resolver.definitionColumnsByKey[cFindDefinitionKey{
		line: lineNo, symbol: symbol,
	}]
}

func (resolver *cPreparedFindScopeResolver) navigationScope(lineNo int) (int, int) {
	if resolver == nil || lineNo < 1 || lineNo > resolver.lineCount {
		return lineNo, lineNo
	}
	var owner int
	if len(resolver.ownerByLine) > 0 {
		owner = resolver.ownerByLine[lineNo]
	} else {
		owner = cFindScopeRunValue(resolver.ownerRuns, lineNo)
	}
	if owner > 0 && owner <= len(resolver.definitions) {
		definition := resolver.definitions[owner-1]
		return definition.scopeStart, definition.scopeEnd
	}
	return resolver.enclosingScope(lineNo)
}

func (resolver *cPreparedFindScopeResolver) navigationScopeAt(
	lineNo, column int,
	structuralLine string,
) (int, int) {
	if resolver != nil && (len(resolver.positionOwners[lineNo]) > 0 ||
		resolver.positionBoundaries.hasLine(lineNo)) {
		owner := 0
		if len(resolver.positionOwners[lineNo]) > 0 {
			owner = resolver.positionOwners.ownerAt(
				resolver.definitions, lineNo, column, structuralLine,
			)
		}
		if owner == 0 {
			owner = resolver.positionFallbackOwner(lineNo)
		}
		owner = resolver.positionBoundaries.ownerAtColumn(
			resolver.definitions, owner, lineNo, column,
		)
		if owner > 0 {
			definition := resolver.definitions[owner-1]
			return definition.scopeStart, definition.scopeEnd
		}
		return lineNo, lineNo
	}
	return resolver.navigationScope(lineNo)
}

func (resolver *cPreparedFindScopeResolver) positionFallbackOwner(lineNo int) int {
	if resolver == nil || lineNo < 1 || lineNo > resolver.lineCount {
		return 0
	}
	if len(resolver.positionFallbackByLine) > 0 {
		return resolver.positionFallbackByLine[lineNo]
	}
	return cFindScopeRunValue(resolver.positionFallbackRuns, lineNo)
}

func (resolver *cPreparedFindScopeResolver) enclosingScope(lineNo int) (int, int) {
	if resolver == nil || lineNo < 1 || lineNo > resolver.lineCount {
		return lineNo, lineNo
	}
	if len(resolver.scopeByLine) > 0 {
		if scope := resolver.scopeByLine[lineNo]; scope.start > 0 {
			return scope.start, scope.end
		}
		return lineNo, lineNo
	}
	scopeIndex := cFindScopeRunValue(resolver.scopeRuns, lineNo)
	if scopeIndex > 0 && scopeIndex <= len(resolver.scopes) {
		scope := resolver.scopes[scopeIndex-1]
		return scope.start, scope.end
	}
	return lineNo, lineNo
}

func (resolver *cPreparedFindScopeResolver) scopeName(lineNo int) string {
	if resolver == nil || lineNo < 1 || lineNo > resolver.lineCount {
		return ""
	}
	var owner int
	if len(resolver.ownerByLine) > 0 {
		owner = resolver.ownerByLine[lineNo]
	} else {
		owner = cFindScopeRunValue(resolver.ownerRuns, lineNo)
	}
	if owner > 0 && owner <= len(resolver.definitions) {
		return resolver.definitions[owner-1].symbol
	}
	return ""
}

func (resolver *cPreparedFindScopeResolver) scopeNameAt(
	lineNo, column int,
	structuralLine string,
) string {
	if resolver != nil && (len(resolver.positionOwners[lineNo]) > 0 ||
		resolver.positionBoundaries.hasLine(lineNo)) {
		owner := 0
		if len(resolver.positionOwners[lineNo]) > 0 {
			owner = resolver.positionOwners.ownerAt(
				resolver.definitions, lineNo, column, structuralLine,
			)
		}
		if owner == 0 {
			owner = resolver.positionFallbackOwner(lineNo)
		}
		owner = resolver.positionBoundaries.ownerAtColumn(
			resolver.definitions, owner, lineNo, column,
		)
		if owner > 0 {
			return resolver.definitions[owner-1].symbol
		}
		return ""
	}
	return resolver.scopeName(lineNo)
}

func cDefinitionOwnerCandidates(
	definitions []sourceDefinition,
	lineCount int,
) []cFindScopeCandidate {
	candidates := make([]cFindScopeCandidate, 0, len(definitions))
	for index, definition := range definitions {
		if !definition.ownsScope || definition.scopeStart > definition.scopeEnd {
			continue
		}
		start := max(1, definition.scopeStart)
		end := min(lineCount, definition.scopeEnd)
		if start > end {
			continue
		}
		candidates = append(candidates, cFindScopeCandidate{
			start: start, end: end, definitionIndex: index,
			size: end - start,
		})
	}
	cSortFindScopeCandidates(candidates)
	return candidates
}

func cPositionFallbackCandidates(
	definitions []sourceDefinition,
	lineCount int,
) []cFindScopeCandidate {
	candidates := make([]cFindScopeCandidate, 0, len(definitions)*2)
	for index, definition := range definitions {
		if !definition.ownsScope || definition.scopeStart > definition.scopeEnd {
			continue
		}
		start := max(1, definition.scopeStart)
		end := min(lineCount, definition.scopeEnd)
		if start > end {
			continue
		}
		size := end - start
		if start < definition.line {
			candidates = append(candidates, cFindScopeCandidate{
				start: start, end: min(end, definition.line-1),
				definitionIndex: index, size: size,
			})
		}
		if definition.line < end {
			candidates = append(candidates, cFindScopeCandidate{
				start: max(start, definition.line+1), end: end,
				definitionIndex: index, size: size,
			})
		}
	}
	cSortFindScopeCandidates(candidates)
	return candidates
}

func cEnclosingScopeCandidates(
	scopes []cLineScope,
	lineCount int,
) []cFindScopeCandidate {
	candidates := make([]cFindScopeCandidate, 0, len(scopes))
	for index, scope := range scopes {
		start := max(1, scope.start)
		end := min(lineCount, scope.end)
		if start > end {
			continue
		}
		candidates = append(candidates, cFindScopeCandidate{
			start: start, end: end, definitionIndex: index,
			size: end - start,
		})
	}
	cSortFindScopeCandidates(candidates)
	return candidates
}

func cSortFindScopeCandidates(candidates []cFindScopeCandidate) {
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
}

func (resolver *cPreparedFindScopeResolver) indexDefinitionOwners(
	candidates []cFindScopeCandidate,
) {
	paintCFindScopeCandidates(
		resolver.ownerByLine,
		candidates,
		cFindScopeUnassignedLines(resolver.lineCount),
	)
}

func (resolver *cPreparedFindScopeResolver) indexEnclosingScopes(
	scopes []cLineScope,
	candidates []cFindScopeCandidate,
) {
	owners := make([]int, resolver.lineCount+1)
	paintCFindScopeCandidates(
		owners,
		candidates,
		cFindScopeUnassignedLines(resolver.lineCount),
	)
	for lineNo := 1; lineNo <= resolver.lineCount; lineNo++ {
		if owners[lineNo] > 0 {
			resolver.scopeByLine[lineNo] = scopes[owners[lineNo]-1]
		}
	}
}

func cBuildFindScopeRuns(
	candidates []cFindScopeCandidate,
	lineCount int,
) []cFindScopeRun {
	if len(candidates) == 0 || lineCount < 1 {
		return nil
	}
	events := make([]cFindScopeEvent, 0, len(candidates)*2)
	for candidateIndex, candidate := range candidates {
		events = append(events, cFindScopeEvent{
			position: candidate.start, candidate: candidateIndex, add: true,
		})
		if candidate.end < lineCount {
			events = append(events, cFindScopeEvent{
				position: candidate.end + 1, candidate: candidateIndex,
			})
		}
	}
	sort.Slice(events, func(first, second int) bool {
		if events[first].position != events[second].position {
			return events[first].position < events[second].position
		}
		if events[first].add != events[second].add {
			return !events[first].add
		}
		return events[first].candidate < events[second].candidate
	})

	active := make([]bool, len(candidates))
	candidateHeap := make([]int, 0, len(candidates))
	runs := make([]cFindScopeRun, 0, len(events))
	for eventIndex := 0; eventIndex < len(events); {
		position := events[eventIndex].position
		nextEvent := eventIndex
		for nextEvent < len(events) && events[nextEvent].position == position {
			event := events[nextEvent]
			active[event.candidate] = event.add
			if event.add {
				candidateHeap = cFindScopeHeapPush(candidateHeap, event.candidate)
			}
			nextEvent++
		}
		candidateHeap = cFindScopeHeapDiscardInactive(candidateHeap, active)
		runEnd := lineCount
		if nextEvent < len(events) {
			runEnd = min(runEnd, events[nextEvent].position-1)
		}
		if position <= runEnd && len(candidateHeap) > 0 {
			value := candidates[candidateHeap[0]].definitionIndex + 1
			runs = cAppendFindScopeRun(runs, cFindScopeRun{
				start: position, end: runEnd, value: value,
			})
		}
		eventIndex = nextEvent
	}
	return runs
}

func cFindScopeHeapPush(candidateHeap []int, candidate int) []int {
	candidateHeap = append(candidateHeap, candidate)
	for child := len(candidateHeap) - 1; child > 0; {
		parent := (child - 1) / 2
		if candidateHeap[parent] <= candidateHeap[child] {
			break
		}
		candidateHeap[parent], candidateHeap[child] = candidateHeap[child], candidateHeap[parent]
		child = parent
	}
	return candidateHeap
}

func cFindScopeHeapDiscardInactive(
	candidateHeap []int,
	active []bool,
) []int {
	for len(candidateHeap) > 0 && !active[candidateHeap[0]] {
		last := len(candidateHeap) - 1
		candidateHeap[0] = candidateHeap[last]
		candidateHeap = candidateHeap[:last]
		for parent := 0; ; {
			left := parent*2 + 1
			if left >= len(candidateHeap) {
				break
			}
			best := left
			right := left + 1
			if right < len(candidateHeap) && candidateHeap[right] < candidateHeap[left] {
				best = right
			}
			if candidateHeap[parent] <= candidateHeap[best] {
				break
			}
			candidateHeap[parent], candidateHeap[best] = candidateHeap[best], candidateHeap[parent]
			parent = best
		}
	}
	return candidateHeap
}

func cAppendFindScopeRun(runs []cFindScopeRun, run cFindScopeRun) []cFindScopeRun {
	if run.start > run.end || run.value < 1 {
		return runs
	}
	if len(runs) > 0 && runs[len(runs)-1].value == run.value &&
		runs[len(runs)-1].end+1 == run.start {
		runs[len(runs)-1].end = run.end
		return runs
	}
	return append(runs, run)
}

func cFindScopeRunValue(runs []cFindScopeRun, lineNo int) int {
	index := sort.Search(len(runs), func(index int) bool {
		return runs[index].end >= lineNo
	})
	if index < len(runs) && runs[index].start <= lineNo {
		return runs[index].value
	}
	return 0
}

func cFindScopeUnassignedLines(lineCount int) []int {
	next := make([]int, lineCount+2)
	for index := range next {
		next[index] = index
	}
	return next
}

func paintCFindScopeCandidates(
	destination []int,
	candidates []cFindScopeCandidate,
	next []int,
) {
	for _, candidate := range candidates {
		for lineNo := cFindScopeNextLine(next, candidate.start); lineNo <= candidate.end; lineNo = cFindScopeNextLine(next, lineNo) {
			destination[lineNo] = candidate.definitionIndex + 1
			next[lineNo] = cFindScopeNextLine(next, lineNo+1)
		}
	}
}

func cFindScopeNextLine(next []int, lineNo int) int {
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
