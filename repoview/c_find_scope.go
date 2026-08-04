package repoview

import "sort"

const cMaximumIndexedFindLines = 256 << 10

// cPreparedFindScopeResolver is an immutable per-file index used by Find and
// navigation. Moderate files use direct line indexes. Larger files use a
// run-length winner index, keeping lookups logarithmic without allocating in
// proportion to an attacker-controlled line count.
type cPreparedFindScopeResolver struct {
	definitionCounts map[cFindDefinitionKey]int
	definitions      []sourceDefinition
	scopes           []cLineScope
	ownerByLine      []int
	scopeByLine      []cLineScope
	ownerRuns        []cFindScopeRun
	scopeRuns        []cFindScopeRun
	lineCount        int
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
		definitions:      definitions,
		definitionCounts: make(map[cFindDefinitionKey]int, len(definitions)),
		lineCount:        lineCount,
	}
	for _, definition := range definitions {
		resolver.definitionCounts[cFindDefinitionKey{
			symbol: definition.symbol,
			line:   definition.line,
		}]++
	}
	ownerCandidates := cDefinitionOwnerCandidates(definitions, lineCount)
	scopeCandidates := cEnclosingScopeCandidates(scopes, lineCount)
	if lineCount <= cMaximumIndexedFindLines {
		resolver.ownerByLine = make([]int, lineCount+1)
		resolver.scopeByLine = make([]cLineScope, lineCount+1)
		resolver.indexDefinitionOwners(ownerCandidates)
		resolver.indexEnclosingScopes(scopes, scopeCandidates)
	} else {
		resolver.scopes = append([]cLineScope(nil), scopes...)
		resolver.ownerRuns = cBuildFindScopeRuns(ownerCandidates, lineCount)
		resolver.scopeRuns = cBuildFindScopeRuns(scopeCandidates, lineCount)
	}
	return resolver
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
