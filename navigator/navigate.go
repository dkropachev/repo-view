package navigator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

const (
	maximumPatchBytes           = 16 << 20
	maximumGitStderrBytes       = 64 << 10
	maximumHunkHeaderBytes      = 512
	maximumChangedSpansPerFile  = 100_000
	maximumChangedLine          = 1_000_000_000
	maximumExpandedChangedLines = 100_000
)

var errSnapshotBudget = errors.New("untracked patch snapshot exceeds byte limit")

var changedHunkPattern = regexp.MustCompile(
	`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`,
)

const (
	maximumGitOutputBytes = 64 << 20
	maximumGitErrorBytes  = 64 << 10
)

var errGitOutputLimit = errors.New("git output exceeds the configured limit")

type boundedOutputBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedOutputBuffer) Write(content []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		return 0, errGitOutputLimit
	}
	if len(content) > remaining {
		written, _ := buffer.Buffer.Write(content[:remaining])
		return written, errGitOutputLimit
	}
	return buffer.Buffer.Write(content)
}

type gitExecutableCloseError struct {
	operation error
	close     error
}

func (failure *gitExecutableCloseError) Error() string {
	return fmt.Sprintf(
		"git invocation and pinned executable close failed: %v; %v",
		failure.operation,
		failure.close,
	)
}

func (failure *gitExecutableCloseError) Unwrap() []error {
	return []error{failure.operation, failure.close}
}

type Return string

const (
	ReturnLocations Return = "locations"
	ReturnLine      Return = "line"
	ReturnContext   Return = "context"
	ReturnScope     Return = "scope"
)

type Include string

const (
	IncludeDefs    Include = "defs"
	IncludeRefs    Include = "refs"
	IncludeBoth    Include = "both"
	IncludeSymbol  Include = "symbol"
	IncludeScope   Include = "scope"
	IncludeImports Include = "imports"
	IncludeAll     Include = "all"
)

// FindMatch selects which repository evidence Find may return.
type FindMatch string

const (
	FindMatchAuto   FindMatch = "auto"
	FindMatchSymbol FindMatch = "symbol"
	FindMatchPath   FindMatch = "path"
)

// FindOutcome reports the category of evidence returned for one query.
type FindOutcome string

const (
	FindOutcomeFile   FindOutcome = "file"
	FindOutcomeSymbol FindOutcome = "symbol"
	FindOutcomeOther  FindOutcome = "other"
	FindOutcomeNone   FindOutcome = "none"
)

// Finding classifies the evidence represented by a Result independently from
// its more specific Kind.
type Finding string

const (
	FindingFile   Finding = "file"
	FindingSymbol Finding = "symbol"
	FindingOther  Finding = "other"
)

type Options struct {
	Return        Return
	Include       Include
	Match         FindMatch
	Base          string
	PathGlobs     []string
	ExcludeGlobs  []string
	MaxCodeLines  int
	Context       int
	Limit         int
	MaxPatchLines int
	// ContextSet distinguishes an explicitly requested zero-line context from
	// the zero value of Options, which retains the default context of five.
	ContextSet     bool
	ChangedOnly    bool
	NoComments     bool
	NoStrings      bool
	DropDocstrings bool
	DropComments   bool
}

type Result struct {
	Scope         string   `json:"scope,omitempty"`
	Symbol        string   `json:"symbol,omitempty"`
	Path          string   `json:"path"`
	Code          string   `json:"code,omitempty"`
	Signature     string   `json:"signature,omitempty"`
	Kind          string   `json:"kind"`
	Finding       Finding  `json:"finding"`
	Language      string   `json:"language,omitempty"`
	ChangedLines  []int    `json:"changed_lines,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	EndLine       int      `json:"end_line,omitempty"`
	StartLine     int      `json:"start_line,omitempty"`
	Line          int      `json:"line,omitempty"`
	CodeStartLine int      `json:"code_start_line,omitempty"`
	CodeEndLine   int      `json:"code_end_line,omitempty"`
	CodeTruncated bool     `json:"code_truncated"`
}

type FindResponse struct {
	Query            string      `json:"query"`
	MatchedAs        FindOutcome `json:"matched_as"`
	Root             string      `json:"root"`
	SearchedAs       []FindMatch `json:"searched_as"`
	Hint             string      `json:"hint,omitempty"`
	Results          []Result    `json:"results"`
	ResultsTruncated bool        `json:"results_truncated"`
}

type InspectResponse struct {
	Location         string   `json:"location"`
	Root             string   `json:"root"`
	Symbol           string   `json:"symbol,omitempty"`
	Error            string   `json:"error,omitempty"`
	Results          []Result `json:"results"`
	ResultsTruncated bool     `json:"results_truncated"`
}

type OutlineResponse struct {
	Path             string   `json:"path"`
	Root             string   `json:"root"`
	Error            string   `json:"error,omitempty"`
	Results          []Result `json:"results"`
	ResultsTruncated bool     `json:"results_truncated"`
}

type ChangedResponse struct {
	Root             string   `json:"root"`
	Base             string   `json:"base,omitempty"`
	BaseCommit       string   `json:"base_commit,omitempty"`
	HeadCommit       string   `json:"head_commit"`
	HeadSubject      string   `json:"head_subject,omitempty"`
	Patch            string   `json:"patch,omitempty"`
	Results          []Result `json:"results"`
	PatchTruncated   bool     `json:"patch_truncated"`
	ResultsTruncated bool     `json:"results_truncated"`
}

type positionedFindScopeResolver interface {
	definitionColumns(lineNo int, symbol string) []int
	navigationScopeAt(lineNo, column int, structuralLine string) (int, int)
	scopeNameAt(lineNo, column int, structuralLine string) string
}

func (r *View) Find(query string, opts Options) (FindResponse, error) {
	return r.find(query, opts, "")
}

// FindSourceFirst behaves like Find but scans one exact repository-relative
// source path first when it is already admitted by the requested filters.
// Other files retain their normal order, and the source path is never injected.
func (r *View) FindSourceFirst(
	query, sourcePath string,
	opts Options,
) (FindResponse, error) {
	clean, err := cleanRepositoryPath(sourcePath)
	if err != nil {
		return FindResponse{}, err
	}
	return r.find(query, opts, clean)
}

func (r *View) find(query string, opts Options, preferredSource string) (FindResponse, error) {
	if err := r.checkContext(); err != nil {
		return FindResponse{}, err
	}
	responses, err := r.findMany([]string{query}, opts, preferredSource)
	if err != nil {
		return FindResponse{}, err
	}
	return responses[0], nil
}

// FindMany locates several exact source identifiers or literal repository
// paths. Auto mode preserves exact-symbol behavior and falls back to path
// lookup only when one query has no symbol results. Limit is shared fairly
// across queries.
func (r *View) FindMany(queries []string, opts Options) ([]FindResponse, error) {
	return r.findMany(queries, opts, "")
}

func (r *View) findMany(
	queries []string,
	opts Options,
	preferredSource string,
) ([]FindResponse, error) {
	if err := r.checkContext(); err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("at least one query is required")
	}
	for _, query := range queries {
		if query == "" {
			return nil, fmt.Errorf("query must not be empty")
		}
	}
	opts = normalizeOptions(opts, ReturnScope)
	if err := validateOptions(opts); err != nil {
		return nil, err
	}

	responses := make([]FindResponse, len(queries))
	var symbolResponses []FindResponse
	var err error
	if opts.Match != FindMatchPath {
		symbolResponses, err = r.findSymbolCandidates(queries, opts, preferredSource)
		if err != nil {
			return nil, err
		}
	}

	pathIndexes := make([]int, 0, len(queries))
	pathQueries := make([]string, 0, len(queries))
	for index, query := range queries {
		if opts.Match == FindMatchPath ||
			(opts.Match == FindMatchAuto && len(symbolResponses[index].Results) == 0) {
			pathIndexes = append(pathIndexes, index)
			pathQueries = append(pathQueries, query)
		}
	}
	pathResponses, err := r.findPathCandidates(pathQueries, opts)
	if err != nil {
		return nil, err
	}
	pathsByIndex := make(map[int]FindResponse, len(pathIndexes))
	for index, response := range pathResponses {
		pathsByIndex[pathIndexes[index]] = response
	}

	for index, query := range queries {
		response := FindResponse{
			Query: query,
			Root:  r.root,
		}
		switch opts.Match {
		case FindMatchSymbol:
			response = symbolResponses[index]
		case FindMatchPath:
			response = pathsByIndex[index]
		case FindMatchAuto:
			if len(symbolResponses[index].Results) != 0 {
				response = symbolResponses[index]
			} else {
				response = pathsByIndex[index]
				response.SearchedAs = []FindMatch{FindMatchSymbol, FindMatchPath}
			}
		}
		if len(response.Results) == 0 {
			response.MatchedAs = FindOutcomeNone
			response.Hint = emptyFindHint(opts.Match)
		}
		responses[index] = response
	}
	responses = limitFindResponses(responses, opts.Limit)
	if err := r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	return responses, nil
}

func (r *View) findSymbolCandidates(
	queries []string,
	opts Options,
	preferredSource string,
) ([]FindResponse, error) {
	files, err := r.filteredFiles(opts)
	if err != nil {
		return nil, err
	}
	files, err = prioritizeExactFile(r.root, files, preferredSource)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	if opts.ChangedOnly {
		var err error
		changed, err = r.changedFileSet(opts)
		if err != nil {
			return nil, err
		}
	}

	type symbolState struct {
		seen    map[string]bool
		symbol  string
		results []Result
	}
	states := make([]symbolState, len(queries))
	for index, symbol := range queries {
		states[index] = symbolState{
			symbol:  symbol,
			results: make([]Result, 0),
			seen:    make(map[string]bool),
		}
	}

	for _, path := range files {
		if err := r.checkContext(); err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(r.root, path)
		rel = filepath.ToSlash(rel)
		if opts.ChangedOnly && !changed[rel] {
			continue
		}
		lines, _, err := r.readRelativeLines(rel)
		if err != nil {
			if errors.Is(err, errRepositoryRootChanged) {
				return nil, err
			}
			continue
		}
		language := prepareLanguageBackend(
			languageForExtension(filepath.Ext(path)),
			lines,
		)
		var definitions []sourceDefinition
		var findScopeResolver preparedFindScopeResolver
		if preparer, ok := language.(findScopeResolverPreparer); ok {
			if prepared := preparer.prepareFindScopeResolver(lines); prepared != nil {
				findScopeResolver = prepared
			}
		}
		if findScopeResolver == nil {
			definitions = language.sourceDefinitions(lines)
		}
		positionedScopeResolver, hasPositionedScopeResolver :=
			findScopeResolver.(positionedFindScopeResolver)
		skipLines := language.ignoredSearchLines(
			lines,
			opts.DropComments || opts.NoComments,
			opts.DropDocstrings,
		)
		noComments := opts.DropComments || opts.NoComments
		searchLines := language.searchLines(lines, noComments, opts.NoStrings)
		if cleaner, ok := language.(optionAwareSearchCleaner); ok {
			searchLines = cleaner.searchSourceLines(
				lines,
				noComments,
				opts.NoStrings,
				opts.DropDocstrings,
			)
		}
		var findSnippet preparedFindSnippet
		findSnippetPrepared := false
		var structuralLines []string
		structuralLinesPrepared := false
		structuralLine := func(lineNo int) string {
			if !structuralLinesPrepared {
				structuralLines = language.searchLines(lines, true, true)
				structuralLinesPrepared = true
			}
			if lineNo < 1 || lineNo > len(structuralLines) {
				return ""
			}
			return structuralLines[lineNo-1]
		}
		processStateLine := func(
			state *symbolState,
			lineOccurrenceCount func(string) int,
			lineOccurrenceColumns func(string) []int,
			lineNo, occurrenceAdjustment int,
			addedColumns, removedColumns []int,
		) bool {
			// No symbol can receive more than the shared limit. Keep one
			// additional result so truncation remains observable.
			if opts.Limit > 0 && len(state.results) > opts.Limit {
				return false
			}
			if lineNo < 1 || lineNo > len(searchLines) || skipLines[lineNo] {
				return true
			}
			line := searchLines[lineNo-1]
			occurrences := lineOccurrenceCount(line)
			switch {
			case occurrenceAdjustment < 0:
				if occurrenceAdjustment <= -occurrences {
					occurrences = 0
				} else {
					occurrences += occurrenceAdjustment
				}
			case occurrenceAdjustment > int(^uint(0)>>1)-occurrences:
				occurrences = int(^uint(0) >> 1)
			default:
				occurrences += occurrenceAdjustment
			}
			if occurrences == 0 {
				return true
			}
			var hitColumns, definitionHitColumns []int
			if hasPositionedScopeResolver {
				hitColumns = lineOccurrenceColumns(line)
				hitColumns = reconcileOccurrenceColumns(
					hitColumns, addedColumns, removedColumns,
				)
				definitionHitColumns = positionedScopeResolver.definitionColumns(
					lineNo, state.symbol,
				)
			}
			var definitionsOnLine int
			if findScopeResolver != nil {
				definitionsOnLine = findScopeResolver.definitionCount(lineNo, state.symbol)
			} else {
				definitionsOnLine = definitionCount(definitions, lineNo, state.symbol)
			}
			matchKinds := []bool{false}
			if definitionsOnLine > 0 {
				matchKinds = []bool{true}
				if occurrences > definitionsOnLine {
					matchKinds = append(matchKinds, false)
				}
			}
			for _, isDefinition := range matchKinds {
				if opts.Limit > 0 && len(state.results) > opts.Limit {
					break
				}
				if !includeKind(opts.Include, isDefinition) {
					continue
				}
				var preparedSnippet *preparedFindSnippet
				if opts.Return != ReturnLocations {
					if !findSnippetPrepared {
						findSnippet = prepareFindSnippet(language, lines, opts)
						findSnippetPrepared = true
					}
					preparedSnippet = &findSnippet
				}
				hitColumn := resultHitColumn(
					hitColumns, definitionHitColumns, isDefinition,
				)
				hitStructuralLine := ""
				if hasPositionedScopeResolver && hitColumn > 0 {
					hitStructuralLine = structuralLine(lineNo)
				}
				result := r.resultForFindHit(
					state.symbol,
					kindForMatch(isDefinition),
					rel,
					language,
					lines,
					lineNo,
					hitColumn,
					hitStructuralLine,
					opts,
					findScopeResolver,
					preparedSnippet,
				)
				key := resultKey(result)
				if state.seen[key] {
					continue
				}
				state.seen[key] = true
				state.results = append(state.results, result)
			}
			return opts.Limit <= 0 || len(state.results) <= opts.Limit
		}

		for stateIndex := range states {
			if err := r.checkContext(); err != nil {
				return nil, err
			}
			state := &states[stateIndex]
			if opts.Limit > 0 && len(state.results) > opts.Limit {
				continue
			}
			lineOccurrenceCount := func(line string) int {
				return countSymbolOccurrences(line, state.symbol)
			}
			lineOccurrenceColumns := func(line string) []int {
				return independentSymbolColumns(line, state.symbol)
			}
			if preparer, ok := language.(symbolOccurrenceCounterPreparer); ok {
				lineOccurrenceCount = preparer.prepareSymbolOccurrenceCounter(state.symbol)
			} else if counter, ok := language.(symbolOccurrenceCounter); ok {
				lineOccurrenceCount = func(line string) int {
					return counter.countSymbolOccurrences(line, state.symbol)
				}
			}
			if resolver, ok := language.(symbolOccurrenceColumnResolver); ok {
				lineOccurrenceColumns = func(line string) []int {
					return resolver.symbolOccurrenceColumns(line, state.symbol)
				}
			}
			if walker, ok := language.(sourceSymbolOccurrencePositionAugmenter); ok &&
				walker.walkAdditionalSymbolOccurrencesAt(
					lines, state.symbol,
					func(
						lineNo, occurrenceAdjustment int,
						addedColumns, removedColumns []int,
					) bool {
						return processStateLine(
							state, lineOccurrenceCount, lineOccurrenceColumns,
							lineNo, occurrenceAdjustment,
							addedColumns, removedColumns,
						)
					},
				) {
				continue
			}
			if walker, ok := language.(sourceSymbolOccurrenceAugmenter); ok &&
				walker.walkAdditionalSymbolOccurrences(
					lines, state.symbol,
					func(lineNo, occurrenceAdjustment int) bool {
						return processStateLine(
							state, lineOccurrenceCount, lineOccurrenceColumns,
							lineNo, occurrenceAdjustment, nil, nil,
						)
					},
				) {
				continue
			}
			for lineIndex := range searchLines {
				if !processStateLine(
					state, lineOccurrenceCount, lineOccurrenceColumns,
					lineIndex+1, 0, nil, nil,
				) {
					break
				}
			}
		}
	}

	responses := make([]FindResponse, 0, len(states))
	for _, state := range states {
		response := FindResponse{
			Query:      state.symbol,
			MatchedAs:  findOutcomeForResults(state.results),
			Root:       r.root,
			SearchedAs: []FindMatch{FindMatchSymbol},
			Results:    state.results,
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func prioritizeExactFile(root string, files []string, preferred string) ([]string, error) {
	if preferred == "" || len(files) < 2 {
		return files, nil
	}
	for index, file := range files {
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return nil, err
		}
		if filepath.ToSlash(relative) != preferred {
			continue
		}
		if index == 0 {
			return files, nil
		}
		ordered := make([]string, 0, len(files))
		ordered = append(ordered, file)
		ordered = append(ordered, files[:index]...)
		ordered = append(ordered, files[index+1:]...)
		return ordered, nil
	}
	return files, nil
}

func (r *View) findPathCandidates(
	queries []string,
	opts Options,
) ([]FindResponse, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	files, err := r.repositoryFiles(false)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	if opts.ChangedOnly {
		changed, err = r.changedFileSet(opts)
		if err != nil {
			return nil, err
		}
	}
	type rankedPath struct {
		path string
		rank int
	}
	responses := make([]FindResponse, 0, len(queries))
	for _, query := range queries {
		matches := make([]rankedPath, 0)
		for _, path := range files {
			if err := r.checkContext(); err != nil {
				return nil, err
			}
			rel, relErr := filepath.Rel(r.root, path)
			if relErr != nil {
				return nil, relErr
			}
			rel = filepath.ToSlash(rel)
			if !matchPathFilters(rel, opts.PathGlobs, opts.ExcludeGlobs) ||
				(opts.ChangedOnly && !changed[rel]) {
				continue
			}
			rank, matched := repositoryPathMatchRank(query, rel)
			if matched {
				matches = append(matches, rankedPath{path: rel, rank: rank})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].rank == matches[j].rank {
				return matches[i].path < matches[j].path
			}
			return matches[i].rank < matches[j].rank
		})
		results := make([]Result, 0, len(matches))
		for _, match := range matches {
			results = append(results, Result{
				Finding: FindingFile,
				Kind:    "file",
				Path:    match.path,
			})
			if opts.Limit > 0 && len(results) > opts.Limit {
				break
			}
		}
		responses = append(responses, FindResponse{
			Query:      query,
			MatchedAs:  FindOutcomeFile,
			Root:       r.root,
			SearchedAs: []FindMatch{FindMatchPath},
			Results:    results,
		})
	}
	return responses, nil
}

func repositoryPathMatchRank(query, path string) (int, bool) {
	query = filepath.ToSlash(query)
	for strings.HasPrefix(query, "./") {
		query = strings.TrimPrefix(query, "./")
	}
	if query == path {
		return 0, true
	}
	base := pathpkg.Base(path)
	if query == base {
		return 1, true
	}
	for _, component := range strings.Split(path, "/") {
		if query == component {
			return 2, true
		}
	}
	if strings.HasPrefix(base, query) {
		return 3, true
	}
	if strings.Contains(base, query) {
		return 4, true
	}
	if strings.Contains(path, query) {
		return 5, true
	}
	return 0, false
}

func limitFindResponses(responses []FindResponse, limit int) []FindResponse {
	remaining := limit
	limited := make([]FindResponse, 0, len(responses))
	for index, response := range responses {
		resultLimit := len(response.Results)
		if limit > 0 {
			resultLimit = fairResultLimit(remaining, len(responses)-index)
			if resultLimit > len(response.Results) {
				resultLimit = len(response.Results)
			}
		}
		response.ResultsTruncated = len(response.Results) > resultLimit
		response.Results = response.Results[:resultLimit]
		limited = append(limited, response)
		if remaining > 0 {
			remaining -= resultLimit
			if remaining <= 0 {
				break
			}
		}
	}
	return limited
}

func emptyFindHint(match FindMatch) string {
	switch match {
	case FindMatchSymbol:
		return "No exact source identifier matched. Try a complete identifier or use match=path for repository paths."
	case FindMatchPath:
		return "No literal repository path matched. Try a shorter path fragment or use match=symbol for source identifiers."
	case FindMatchAuto:
		return "No exact source identifier or literal repository path matched. Try a shorter literal query, or use changed for branch, commit, or PR work."
	default:
		return "No repository evidence matched."
	}
}

func fairResultLimit(remaining, remainingSelectors int) int {
	limit := remaining / remainingSelectors
	if remaining%remainingSelectors != 0 {
		limit++
	}
	return limit
}

func (r *View) Inspect(location string, opts Options) (InspectResponse, error) {
	if err := r.checkContext(); err != nil {
		return InspectResponse{}, err
	}
	requestedPath, lineNo, err := parseLocation(location)
	if err != nil {
		return InspectResponse{}, err
	}
	opts = normalizeOptions(opts, ReturnScope)
	if err := validateOptions(opts); err != nil {
		return InspectResponse{}, err
	}
	lines, path, err := r.readRelativeLines(requestedPath)
	if err != nil {
		return InspectResponse{}, err
	}
	if !matchPathFilters(path, opts.PathGlobs, opts.ExcludeGlobs) {
		return InspectResponse{}, fmt.Errorf(
			"repository path %q is excluded by the requested path filters",
			path,
		)
	}
	if opts.ChangedOnly {
		changed, changedErr := r.changedFileSet(opts)
		if changedErr != nil {
			return InspectResponse{}, changedErr
		}
		if !changed[path] {
			return InspectResponse{}, fmt.Errorf(
				"repository path %q is not changed from the requested base",
				path,
			)
		}
	}
	if lineNo < 1 || lineNo > len(lines) {
		return InspectResponse{}, fmt.Errorf("line %d out of range for %s", lineNo, path)
	}
	language := prepareLanguageBackend(languageForExtension(filepath.Ext(path)), lines)
	symbol := bestSymbolOnLine(lines, lineNo, language)
	results := []Result{
		r.resultForHit(symbol, "scope", filepath.ToSlash(path), language, lines, lineNo, opts),
	}
	if opts.Include == IncludeImports || opts.Include == IncludeAll {
		if result, ok := importResult(filepath.ToSlash(path), language, lines, opts); ok {
			results = append(results, result)
		}
	}
	if wantsInspectRelated(opts.Include) && symbol != "" {
		relatedInclude := opts.Include
		if relatedInclude == IncludeAll {
			relatedInclude = IncludeBoth
		}
		relatedLimit := opts.Limit
		if relatedLimit > 0 {
			relatedLimit -= len(results)
			if relatedLimit < 0 {
				return InspectResponse{
					Location:         location,
					Root:             r.root,
					Symbol:           symbol,
					Results:          results[:opts.Limit],
					ResultsTruncated: true,
				}, nil
			}
			if relatedLimit == 0 {
				// The initial results exactly fill the limit. Probe for one
				// related result so truncation reflects an actual omission.
				relatedLimit = 1
			}
		}
		related, err := r.Find(symbol, Options{
			Include:        relatedInclude,
			Match:          FindMatchSymbol,
			Return:         opts.Return,
			Context:        opts.Context,
			ContextSet:     opts.ContextSet,
			Limit:          relatedLimit,
			PathGlobs:      opts.PathGlobs,
			ExcludeGlobs:   opts.ExcludeGlobs,
			ChangedOnly:    opts.ChangedOnly,
			Base:           opts.Base,
			DropComments:   opts.DropComments,
			DropDocstrings: opts.DropDocstrings,
			NoComments:     opts.NoComments,
			NoStrings:      opts.NoStrings,
			MaxCodeLines:   opts.MaxCodeLines,
			MaxPatchLines:  opts.MaxPatchLines,
		})
		if err != nil {
			return InspectResponse{}, err
		}
		responseTruncated := related.ResultsTruncated
		results = append(results, related.Results...)
		results = dedupeResults(results)
		if opts.Limit > 0 && len(results) > opts.Limit {
			results = results[:opts.Limit]
			responseTruncated = true
		}
		return InspectResponse{
			Location:         location,
			Root:             r.root,
			Symbol:           symbol,
			Results:          results,
			ResultsTruncated: responseTruncated,
		}, nil
	}
	results = dedupeResults(results)
	resultsTruncated := false
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
		resultsTruncated = true
	}
	return InspectResponse{
		Location:         location,
		Root:             r.root,
		Symbol:           symbol,
		Results:          results,
		ResultsTruncated: resultsTruncated,
	}, nil
}

func (r *View) Outline(path string, opts Options) (OutlineResponse, error) {
	if err := r.checkContext(); err != nil {
		return OutlineResponse{}, err
	}
	opts = normalizeOptions(opts, ReturnLine)
	if err := validateOptions(opts); err != nil {
		return OutlineResponse{}, err
	}
	lines, path, err := r.readRelativeLines(path)
	if err != nil {
		return OutlineResponse{}, err
	}
	language := prepareLanguageBackend(languageForExtension(filepath.Ext(path)), lines)
	definitions := language.sourceDefinitions(lines)
	results := make([]Result, 0, len(definitions))
	for _, definition := range definitions {
		if err := r.checkContext(); err != nil {
			return OutlineResponse{}, err
		}
		lineNo := definition.line
		symbol := definition.symbol
		result := r.resultForHit(symbol, "def", filepath.ToSlash(path), language, lines, lineNo, opts)
		result.Signature = strings.TrimSpace(lines[lineNo-1])
		if opts.Limit > 0 && len(results) >= opts.Limit {
			return OutlineResponse{
				Path:             filepath.ToSlash(path),
				Root:             r.root,
				Results:          results,
				ResultsTruncated: true,
			}, nil
		}
		results = append(results, result)
	}
	return OutlineResponse{Path: filepath.ToSlash(path), Root: r.root, Results: results}, nil
}

func (r *View) Changed(opts Options) (ChangedResponse, error) {
	if err := r.checkContext(); err != nil {
		return ChangedResponse{}, err
	}
	opts = normalizeOptions(opts, ReturnContext)
	if err := validateOptions(opts); err != nil {
		return ChangedResponse{}, err
	}
	if err := r.verifyRootIdentity(); err != nil {
		return ChangedResponse{}, err
	}
	baseCommit, err := r.resolveBase(opts.Base)
	if err != nil {
		return ChangedResponse{}, err
	}
	headCommit, err := r.resolveOptionalHead()
	if err != nil {
		return ChangedResponse{}, fmt.Errorf("resolve git HEAD: %w", err)
	}
	var headSubject string
	if headCommit != "" {
		headSubject, err = r.gitText("show", "-s", "--format=%s", headCommit)
		if err != nil {
			return ChangedResponse{}, fmt.Errorf("read git HEAD subject: %w", err)
		}
	}
	if baseCommit != "" && headCommit == "" {
		return ChangedResponse{}, fmt.Errorf("compare Git base without a HEAD commit")
	}
	files, err := r.changedFiles(baseCommit, headCommit)
	if err != nil {
		return ChangedResponse{}, err
	}
	response := ChangedResponse{
		Root:        r.root,
		Base:        opts.Base,
		BaseCommit:  baseCommit,
		HeadCommit:  headCommit,
		HeadSubject: headSubject,
	}
	var selectedFiles []string
	for _, rel := range files {
		if err := r.checkContext(); err != nil {
			return ChangedResponse{}, err
		}
		if matchPathFilters(rel, opts.PathGlobs, opts.ExcludeGlobs) {
			selectedFiles = append(selectedFiles, rel)
		}
	}
	patch, patchTruncated, err := r.changedPatch(
		baseCommit,
		headCommit,
		selectedFiles,
		opts.MaxPatchLines,
	)
	if err != nil {
		return ChangedResponse{}, err
	}
	results := make([]Result, 0)
	seenResults := make(map[string]bool)
	appendResult := func(result Result) bool {
		key := resultKey(result)
		if !seenResults[key] {
			seenResults[key] = true
			results = append(results, result)
		}
		return opts.Limit <= 0 || len(results) <= opts.Limit
	}

changedFiles:
	for _, rel := range selectedFiles {
		if err := r.checkContext(); err != nil {
			return ChangedResponse{}, err
		}
		language := languageForExtension(filepath.Ext(rel))
		var lines []string
		var cleanRel string
		var readErr error
		if baseCommit != "" {
			lines, cleanRel, readErr = r.readGitLinesAtRevision(rel, response.HeadCommit)
		} else {
			lines, cleanRel, readErr = r.readRelativeLines(rel)
		}
		if errors.Is(readErr, errRepositoryRootChanged) {
			return ChangedResponse{}, readErr
		}
		if readErr != nil || len(lines) == 0 {
			if !appendResult(Result{
				Finding: FindingFile, Kind: "file", Path: rel, Language: language.name(),
			}) {
				break
			}
			continue
		}
		language = prepareLanguageBackend(language, lines)
		rel = cleanRel
		lineNumbers, err := r.changedLines(rel, baseCommit, headCommit, len(lines))
		if err != nil {
			return ChangedResponse{}, err
		}
		if len(lineNumbers) == 0 {
			if !appendResult(Result{
				Finding: FindingFile, Kind: "file", Path: rel, Language: language.name(),
			}) {
				break
			}
			continue
		}
		if opts.Return == ReturnLocations {
			for _, span := range mergeContextRanges(len(lines), lineNumbers, 0) {
				if !appendResult(r.resultForRange(
					"", "changed", rel, language, lines, span[0], span[1],
					linesInRange(lineNumbers, span[0], span[1]), opts,
				)) {
					break changedFiles
				}
			}
			continue
		}
		if opts.Return == ReturnContext {
			for _, span := range mergeContextRanges(len(lines), lineNumbers, opts.Context) {
				if !appendResult(r.resultForRange(
					"", "changed", rel, language, lines, span[0], span[1],
					linesInRange(lineNumbers, span[0], span[1]), opts,
				)) {
					break changedFiles
				}
			}
			continue
		}
		for _, lineNo := range lineNumbers {
			if lineNo < 1 || lineNo > len(lines) {
				lineNo = 1
			}
			if !appendResult(r.resultForHit(
				"", "changed", rel, language, lines, lineNo, opts,
			)) {
				break changedFiles
			}
		}
	}
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
		response.ResultsTruncated = true
	}
	response.Patch = patch
	response.PatchTruncated = patchTruncated
	response.Results = results
	if err := r.verifyRootIdentity(); err != nil {
		return ChangedResponse{}, err
	}
	return response, nil
}

func (r *View) resultForRange(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	start, end int,
	hitLines []int,
	opts Options,
) Result {
	lineNo := start
	result := Result{
		Finding:      findingForKind(kind),
		Kind:         kind,
		Symbol:       symbol,
		Path:         rel,
		Line:         lineNo,
		StartLine:    start,
		EndLine:      end,
		Language:     language.name(),
		ChangedLines: append([]int(nil), hitLines...),
	}
	scopes := scopeNamesForLines(lines, hitLines, language)
	if len(scopes) == 1 {
		result.Scope = scopes[0]
	} else if len(scopes) > 1 {
		result.Scopes = scopes
	}
	if kind == "def" {
		result.Scope = symbol
	}
	if opts.Return != ReturnLocations {
		focus := start
		if len(hitLines) > 0 {
			focus = hitLines[0]
		}
		result.Code, result.CodeStartLine, result.CodeEndLine, result.CodeTruncated =
			resultSnippet(language, lines, start, end, focus, opts)
	}
	return result
}

func (r *View) resultForHit(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	lineNo int,
	opts Options,
) Result {
	return r.resultForFindHit(
		symbol, kind, rel, language, lines, lineNo, 0, "", opts, nil, nil,
	)
}

func (r *View) resultForFindHit(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	lineNo, hitColumn int,
	structuralLine string,
	opts Options,
	findScopeResolver preparedFindScopeResolver,
	preparedSnippet *preparedFindSnippet,
) Result {
	start, end := lineNo, lineNo
	switch opts.Return {
	case ReturnScope:
		if positioned, ok := findScopeResolver.(positionedFindScopeResolver); ok &&
			hitColumn > 0 {
			start, end = positioned.navigationScopeAt(
				lineNo, hitColumn, structuralLine,
			)
		} else if findScopeResolver != nil {
			start, end = findScopeResolver.navigationScope(lineNo)
		} else if resolver, ok := language.(navigationScopeResolver); ok {
			start, end = resolver.navigationScope(lines, lineNo)
		} else {
			start, end = language.enclosingScope(lines, lineNo)
		}
	case ReturnContext:
		start, end = contextRange(len(lines), lineNo, opts.Context)
	case ReturnLine, ReturnLocations:
		start, end = lineNo, lineNo
	}
	var scope string
	switch {
	case kind == "def":
		scope = symbol
	case hitColumn > 0:
		if positioned, ok := findScopeResolver.(positionedFindScopeResolver); ok {
			scope = positioned.scopeNameAt(lineNo, hitColumn, structuralLine)
		} else if findScopeResolver != nil {
			scope = findScopeResolver.scopeName(lineNo)
		} else {
			scope = scopeName(lines, lineNo, language)
		}
	case findScopeResolver != nil:
		scope = findScopeResolver.scopeName(lineNo)
	default:
		scope = scopeName(lines, lineNo, language)
	}
	result := Result{
		Finding:   findingForKind(kind),
		Kind:      kind,
		Symbol:    symbol,
		Path:      rel,
		Line:      lineNo,
		StartLine: start,
		EndLine:   end,
		Language:  language.name(),
		Scope:     scope,
	}
	if opts.Return != ReturnLocations {
		result.Code, result.CodeStartLine, result.CodeEndLine, result.CodeTruncated =
			resultSnippetWithPrepared(
				language, lines, start, end, lineNo, opts, preparedSnippet,
			)
	}
	return result
}

func independentSymbolColumns(line, symbol string) []int {
	if line == "" || symbol == "" || len(symbol) > len(line) {
		return nil
	}
	var columns []int
	for offset := 0; offset <= len(line)-len(symbol); {
		relative := strings.Index(line[offset:], symbol)
		if relative < 0 {
			break
		}
		position := offset + relative
		before, _ := utf8.DecodeLastRuneInString(line[:position])
		afterIndex := position + len(symbol)
		after, _ := utf8.DecodeRuneInString(line[afterIndex:])
		if (position == 0 || !isIdent(before)) &&
			(afterIndex == len(line) || !isIdent(after)) {
			columns = append(columns, position+1)
		}
		_, size := utf8.DecodeRuneInString(line[position:])
		offset = position + max(1, size)
	}
	return columns
}

func resultHitColumn(
	occurrenceColumns, definitionColumns []int,
	isDefinition bool,
) int {
	if isDefinition {
		if len(definitionColumns) > 0 {
			return definitionColumns[0]
		}
		return 0
	}
	if len(occurrenceColumns) == 0 {
		return 0
	}
	if len(definitionColumns) == 0 {
		return occurrenceColumns[0]
	}
	definitionCounts := make(map[int]int, len(definitionColumns))
	for _, column := range definitionColumns {
		definitionCounts[column]++
	}
	for _, column := range occurrenceColumns {
		if definitionCounts[column] > 0 {
			definitionCounts[column]--
			continue
		}
		return column
	}
	return 0
}

func reconcileOccurrenceColumns(
	columns, addedColumns, removedColumns []int,
) []int {
	if len(addedColumns) == 0 && len(removedColumns) == 0 {
		return columns
	}
	removed := make(map[int]int, len(removedColumns))
	for _, column := range removedColumns {
		if column > 0 {
			removed[column]++
		}
	}
	corrected := make([]int, 0, len(columns)+len(addedColumns))
	for _, column := range columns {
		if removed[column] > 0 {
			removed[column]--
			continue
		}
		corrected = append(corrected, column)
	}
	for _, column := range addedColumns {
		if column > 0 {
			corrected = append(corrected, column)
		}
	}
	sort.Ints(corrected)
	unique := corrected[:0]
	for _, column := range corrected {
		if len(unique) == 0 || unique[len(unique)-1] != column {
			unique = append(unique, column)
		}
	}
	return unique
}

type preparedFindSnippet struct {
	lines                   []string
	linePreservingAttempted bool
	cleanedWholeSource      bool
}

func prepareFindSnippet(
	language languageBackend,
	lines []string,
	opts Options,
) preparedFindSnippet {
	cleaner, ok := language.(linePreservingSourceCleaner)
	if !ok || !opts.DropComments && !opts.DropDocstrings {
		return preparedFindSnippet{}
	}
	prepared := preparedFindSnippet{linePreservingAttempted: true}
	cleaned := cleaner.cleanSourceLines(
		lines, opts.DropComments, opts.DropDocstrings,
	)
	if len(cleaned) == len(lines) {
		prepared.lines = cleaned
		prepared.cleanedWholeSource = true
	}
	return prepared
}

func resultSnippet(
	language languageBackend,
	lines []string,
	start, end, focus int,
	opts Options,
) (string, int, int, bool) {
	return resultSnippetWithPrepared(
		language, lines, start, end, focus, opts, nil,
	)
}

func resultSnippetWithPrepared(
	language languageBackend,
	lines []string,
	start, end, focus int,
	opts Options,
	prepared *preparedFindSnippet,
) (string, int, int, bool) {
	snippetLines := lines
	cleanedWholeSource := false
	linePreservingAttempted := prepared != nil && prepared.linePreservingAttempted
	if prepared != nil && prepared.cleanedWholeSource {
		snippetLines = prepared.lines
		cleanedWholeSource = true
	} else if !linePreservingAttempted {
		if cleaner, ok := language.(linePreservingSourceCleaner); ok &&
			(opts.DropComments || opts.DropDocstrings) {
			cleaned := cleaner.cleanSourceLines(
				lines, opts.DropComments, opts.DropDocstrings,
			)
			if len(cleaned) == len(lines) {
				snippetLines = cleaned
				cleanedWholeSource = true
			}
		}
	}
	code, codeStart, codeEnd, truncated := snippet(
		snippetLines,
		start,
		end,
		focus,
		opts.MaxCodeLines,
	)
	if !cleanedWholeSource {
		code = language.cleanSource(code, opts.DropComments, opts.DropDocstrings)
	}
	if finalizer, ok := language.(sourceSnippetFinalizer); ok {
		code = finalizer.finalizeSourceSnippet(
			code,
			opts.DropComments,
			opts.DropDocstrings,
		)
	}
	return strings.TrimRight(code, "\n"), codeStart, codeEnd, truncated
}

func normalizeOptions(opts Options, defaultReturn Return) Options {
	if opts.Context == 0 && !opts.ContextSet {
		opts.Context = 5
	}
	if opts.Include == "" {
		opts.Include = IncludeBoth
	}
	if opts.Match == "" {
		opts.Match = FindMatchAuto
	}
	if opts.Return == "" {
		opts.Return = defaultReturn
	}
	if opts.MaxCodeLines == 0 {
		opts.MaxCodeLines = 80
	}
	if opts.MaxPatchLines == 0 {
		opts.MaxPatchLines = 400
	}
	return opts
}

func validateOptions(opts Options) error {
	switch opts.Return {
	case ReturnLocations, ReturnLine, ReturnContext, ReturnScope:
	default:
		return fmt.Errorf("return must be one of: locations, line, context, scope")
	}
	switch opts.Include {
	case IncludeDefs, IncludeRefs, IncludeBoth, IncludeSymbol, IncludeScope, IncludeImports, IncludeAll:
	default:
		return fmt.Errorf("include must be one of: defs, refs, both, symbol, scope, imports, all")
	}
	switch opts.Match {
	case FindMatchAuto, FindMatchSymbol, FindMatchPath:
	default:
		return fmt.Errorf("match must be one of: auto, symbol, path")
	}
	if opts.Context < 0 {
		return fmt.Errorf("context must be non-negative")
	}
	if opts.Limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if opts.MaxCodeLines < 1 {
		return fmt.Errorf("max-code-lines must be positive")
	}
	if opts.MaxPatchLines < 1 {
		return fmt.Errorf("max-patch-lines must be positive")
	}
	return nil
}

func includeKind(include Include, isDef bool) bool {
	switch include {
	case IncludeDefs:
		return isDef
	case IncludeRefs:
		return !isDef
	case IncludeBoth, IncludeSymbol, IncludeScope, IncludeImports, IncludeAll, "":
		return true
	default:
		return true
	}
}

func kindForMatch(isDef bool) string {
	if isDef {
		return "def"
	}
	return "ref"
}

func findingForKind(kind string) Finding {
	switch kind {
	case "file":
		return FindingFile
	case "def", "ref":
		return FindingSymbol
	default:
		return FindingOther
	}
}

func findOutcomeForResults(results []Result) FindOutcome {
	for _, result := range results {
		if result.Finding == FindingSymbol {
			return FindOutcomeSymbol
		}
	}
	if len(results) != 0 {
		return FindOutcomeOther
	}
	return FindOutcomeNone
}

func (r *View) filteredFiles(opts Options) ([]string, error) {
	files, err := r.sourceFiles()
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, path := range files {
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return nil, err
		}
		if matchPathFilters(filepath.ToSlash(rel), opts.PathGlobs, opts.ExcludeGlobs) {
			out = append(out, path)
		}
	}
	return out, nil
}

func matchPathFilters(path string, includes, excludes []string) bool {
	for _, pattern := range excludes {
		if globMatch(pattern, path) {
			return false
		}
	}
	if len(includes) == 0 {
		return true
	}
	for _, pattern := range includes {
		if globMatch(pattern, path) {
			return true
		}
	}
	return false
}

func globMatch(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	pattern = strings.TrimPrefix(pattern, "**/")
	if !strings.Contains(pattern, "/") {
		if ok, _ := pathpkg.Match(pattern, pathpkg.Base(path)); ok {
			return true
		}
	}
	if ok, _ := pathpkg.Match(pattern, path); ok {
		return true
	}
	if strings.Contains(path, pattern) {
		return true
	}
	return false
}

func contextRange(total, lineNo, context int) (int, int) {
	start := 1
	if context < lineNo-1 {
		start = lineNo - context
	}
	end := total
	if context < total-lineNo {
		end = lineNo + context
	}
	return start, end
}

func mergeContextRanges(total int, lines []int, context int) [][2]int {
	if len(lines) == 0 {
		return nil
	}
	var spans [][2]int
	for _, lineNo := range lines {
		start, end := contextRange(total, lineNo, context)
		if len(spans) == 0 || start > spans[len(spans)-1][1]+1 {
			spans = append(spans, [2]int{start, end})
			continue
		}
		if end > spans[len(spans)-1][1] {
			spans[len(spans)-1][1] = end
		}
	}
	return spans
}

func snippet(lines []string, start, end, focus, maxLines int) (string, int, int, bool) {
	codeStart, codeEnd := start, end
	truncated := maxLines > 0 && end-start+1 > maxLines
	if truncated {
		if focus < start || focus > end {
			focus = start
		}
		codeStart = focus - maxLines/2
		if codeStart < start {
			codeStart = start
		}
		codeEnd = codeStart + maxLines - 1
		if codeEnd > end {
			codeEnd = end
			codeStart = codeEnd - maxLines + 1
		}
	}
	code := strings.Join(lines[codeStart-1:codeEnd], "\n")
	if !truncated {
		return code, 0, 0, false
	}
	return code, codeStart, codeEnd, true
}

func parseLocation(location string) (string, int, error) {
	idx := strings.LastIndex(location, ":")
	if idx < 1 || idx == len(location)-1 {
		return "", 0, fmt.Errorf("location must be PATH:LINE")
	}
	lineNo, err := strconv.Atoi(location[idx+1:])
	if err != nil {
		return "", 0, fmt.Errorf("location must be PATH:LINE: %w", err)
	}
	return location[:idx], lineNo, nil
}

func bestSymbolOnLine(lines []string, lineNo int, language languageBackend) string {
	for _, definition := range language.sourceDefinitions(lines) {
		if definition.line == lineNo {
			return definition.symbol
		}
	}
	if resolver, ok := language.(interface {
		symbolOnLine([]string, int) (string, bool)
	}); ok {
		if symbol, found := resolver.symbolOnLine(lines, lineNo); found {
			return symbol
		}
		if _, authoritative := language.(authoritativeSymbolOnLineResolver); authoritative {
			return ""
		}
	}
	line := language.searchLines(lines, true, true)[lineNo-1]
	if symbol, ok := language.definitionSymbol(line); ok {
		return symbol
	}
	identifier := `[\p{L}_][\p{L}\p{Nd}_]*`
	member := regexp.MustCompile(`\.\s*(` + identifier + `)`)
	if match := member.FindStringSubmatch(line); len(match) == 2 {
		return match[1]
	}
	directCall := regexp.MustCompile(`(` + identifier + `)\s*(?:\[|\()`)
	for _, match := range directCall.FindAllStringSubmatch(line, -1) {
		if len(match) == 2 && match[1] != "_" && !isKeyword(match[1]) {
			return match[1]
		}
	}
	ident := regexp.MustCompile(identifier)
	for _, symbol := range ident.FindAllString(line, -1) {
		if symbol != "_" && !isKeyword(symbol) {
			return symbol
		}
	}
	return ""
}

func scopeName(lines []string, lineNo int, language languageBackend) string {
	if resolver, ok := language.(sourceScopeNameResolver); ok {
		if symbol, handled := resolver.scopeNameOnLine(lines, lineNo); handled {
			return symbol
		}
	}
	definitions := language.sourceDefinitions(lines)
	bestSymbol := ""
	bestSize := 0
	bestLine := 0
	bestBefore := false
	for _, definition := range definitions {
		if !definition.ownsScope {
			continue
		}
		if lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
			continue
		}
		size := definition.scopeEnd - definition.scopeStart
		before := definition.line <= lineNo
		if bestSymbol == "" || before && !bestBefore || before == bestBefore &&
			(size < bestSize || size == bestSize &&
				(before && definition.line > bestLine || !before && definition.line < bestLine)) {
			bestSymbol = definition.symbol
			bestSize = size
			bestLine = definition.line
			bestBefore = before
		}
	}
	if bestSymbol != "" {
		return bestSymbol
	}
	start, end := language.enclosingScope(lines, lineNo)
	for pos := min(lineNo, end); pos >= start; pos-- {
		for _, definition := range definitions {
			if definition.ownsScope && definition.line == pos {
				return definition.symbol
			}
		}
	}
	forwardEnd := end
	current := strings.TrimSpace(lines[lineNo-1])
	if start == lineNo && end == lineNo &&
		(strings.HasPrefix(current, "//") || strings.HasPrefix(current, "/*") ||
			strings.HasPrefix(current, "*") || strings.HasPrefix(current, "#")) {
		forwardEnd = len(lines)
	}
	for pos := lineNo + 1; pos <= min(forwardEnd, len(lines)); pos++ {
		trimmed := strings.TrimSpace(lines[pos-1])
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, definition := range definitions {
			if definition.ownsScope && definition.line == pos {
				return definition.symbol
			}
		}
		break
	}
	return ""
}

func scopeNamesForLines(lines []string, lineNumbers []int, language languageBackend) []string {
	seen := make(map[string]bool)
	var scopes []string
	for _, lineNo := range lineNumbers {
		if lineNo < 1 || lineNo > len(lines) {
			continue
		}
		scope := scopeName(lines, lineNo, language)
		if scope != "" && !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func linesInRange(lineNumbers []int, start, end int) []int {
	var selected []int
	for _, lineNo := range lineNumbers {
		if start <= lineNo && lineNo <= end {
			selected = append(selected, lineNo)
		}
	}
	return selected
}

func wantsInspectRelated(include Include) bool {
	return include == IncludeDefs || include == IncludeRefs || include == IncludeBoth || include == IncludeAll
}

func importResult(
	path string,
	language languageBackend,
	lines []string,
	opts Options,
) (Result, bool) {
	start, end, ok := language.importRange(lines)
	if !ok {
		return Result{}, false
	}
	result := Result{
		Finding:   FindingOther,
		Kind:      "imports",
		Path:      path,
		StartLine: start,
		EndLine:   end,
		Language:  language.name(),
	}
	if opts.Return != ReturnLocations {
		codeStart, codeEnd := start, end
		switch opts.Return {
		case ReturnLine:
			codeEnd = codeStart
		case ReturnContext:
			codeStart, _ = contextRange(len(lines), start, opts.Context)
			_, codeEnd = contextRange(len(lines), end, opts.Context)
		case ReturnScope, ReturnLocations:
		}
		result.Code, result.CodeStartLine, result.CodeEndLine, result.CodeTruncated =
			resultSnippet(language, lines, codeStart, codeEnd, start, opts)
		if !result.CodeTruncated && (codeStart != start || codeEnd != end) {
			result.CodeStartLine = codeStart
			result.CodeEndLine = codeEnd
		}
	}
	return result, true
}

func isKeyword(symbol string) bool {
	switch symbol {
	case "and", "auto", "bool", "break", "case", "class", "const", "continue", "def", "defer",
		"else", "enum", "false", "for", "func", "function", "if", "impl", "import", "int",
		"let", "match", "nil", "none", "null", "package", "private", "protected", "pub",
		"public", "return", "self", "static", "struct", "switch", "this", "true", "type",
		"uint", "use", "var", "void", "while":
		return true
	default:
		return false
	}
}

func dedupeResults(results []Result) []Result {
	seen := map[string]bool{}
	out := make([]Result, 0, len(results))
	for _, result := range results {
		key := resultKey(result)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, result)
	}
	return out
}

func resultKey(result Result) string {
	return fmt.Sprintf(
		"%s:%s:%d:%d:%s",
		result.Kind,
		result.Path,
		result.StartLine,
		result.EndLine,
		result.Symbol,
	)
}

func (r *View) changedFileSet(opts Options) (map[string]bool, error) {
	if err := r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	baseCommit, err := r.resolveBase(opts.Base)
	if err != nil {
		return nil, err
	}
	headCommit, err := r.resolveOptionalHead()
	if err != nil {
		return nil, err
	}
	if baseCommit != "" && headCommit == "" {
		return nil, fmt.Errorf("cannot compare Git base without a HEAD commit")
	}
	files, err := r.changedFiles(baseCommit, headCommit)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, file := range files {
		out[file] = true
	}
	return out, nil
}

func (r *View) changedFiles(base, head string) ([]string, error) {
	if base != "" {
		return r.gitFileList(gitdiffcontract.NameOnlyArguments(base, head)...)
	}
	staged := []string{
		"diff", "--cached", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--name-only", "-z",
	}
	if head != "" {
		staged = append(staged, head)
	}
	commands := [][]string{
		staged,
		{"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--name-only", "-z"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	}
	seen := map[string]bool{}
	var files []string
	for _, args := range commands {
		list, err := r.gitFileList(args...)
		if err != nil {
			return nil, err
		}
		for _, file := range list {
			if !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

type patchOutputCollector struct {
	output          []byte
	maxLines        int
	maxBytes        int
	lines           int
	pendingNewlines int
	truncated       bool
}

func newPatchOutputCollector(maxLines, maxBytes int) *patchOutputCollector {
	return &patchOutputCollector{maxLines: maxLines, maxBytes: maxBytes}
}

func (c *patchOutputCollector) appendFragment(fragment []byte) bool {
	if len(fragment) == 0 {
		return true
	}
	if len(c.output) > 0 {
		if !c.consume([]byte{'\n'}) {
			return false
		}
	}
	return c.consume(fragment)
}

func (c *patchOutputCollector) consume(input []byte) bool {
	for _, character := range input {
		if character == '\n' {
			c.pendingNewlines++
			// A completed command's trailing newlines are trimmed, so defer
			// accounting until a later non-newline proves they are part of the
			// patch. The extra bounded probe prevents an adversarial stream of
			// only newlines from running forever.
			if c.pendingNewlines > c.maxBytes+1 {
				allowedNewlines := c.maxLines - c.lines
				if len(c.output) == 0 {
					allowedNewlines = c.maxLines - 1
				}
				c.appendNewlines(min(
					allowedNewlines,
					c.maxBytes-len(c.output),
				))
				c.pendingNewlines = 0
				c.truncated = true
				return false
			}
			continue
		}

		candidateLines := c.lines + c.pendingNewlines
		if len(c.output) == 0 {
			candidateLines = c.pendingNewlines + 1
		}
		if candidateLines > c.maxLines {
			allowedNewlines := c.maxLines - c.lines
			if len(c.output) == 0 {
				allowedNewlines = c.maxLines - 1
			}
			c.appendNewlines(min(allowedNewlines, c.maxBytes-len(c.output)))
			c.pendingNewlines = 0
			c.truncated = true
			return false
		}

		requiredBytes := c.pendingNewlines + 1
		remainingBytes := c.maxBytes - len(c.output)
		if requiredBytes > remainingBytes {
			newlines := min(c.pendingNewlines, remainingBytes)
			c.appendNewlines(newlines)
			remainingBytes -= newlines
			if remainingBytes > 0 {
				c.output = append(c.output, character)
			}
			c.pendingNewlines = 0
			c.truncated = true
			return false
		}

		wasEmpty := len(c.output) == 0
		c.appendNewlines(c.pendingNewlines)
		c.output = append(c.output, character)
		if wasEmpty {
			c.lines = c.pendingNewlines + 1
		} else {
			c.lines += c.pendingNewlines
		}
		c.pendingNewlines = 0
	}
	return true
}

func (c *patchOutputCollector) appendNewlines(count int) {
	for range max(0, count) {
		c.output = append(c.output, '\n')
	}
}

func (c *patchOutputCollector) commitPendingNewlines() {
	remaining := c.maxBytes - len(c.output)
	c.appendNewlines(min(c.pendingNewlines, max(0, remaining)))
	c.pendingNewlines = 0
}

type boundedByteWriter struct {
	data  []byte
	limit int
}

func (w *boundedByteWriter) Write(input []byte) (int, error) {
	remaining := max(0, w.limit-len(w.data))
	w.data = append(w.data, input[:min(len(input), remaining)]...)
	return len(input), nil
}

func boundedPatchCommandOutput(
	cmd *exec.Cmd,
	maxLines, maxBytes int,
	allowExitOne bool,
) ([]byte, bool, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	stderr := &boundedByteWriter{limit: maximumGitStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}

	collector := newPatchOutputCollector(maxLines, maxBytes)
	buffer := make([]byte, 32<<10)
	var readErr error
	for {
		count, currentErr := stdout.Read(buffer)
		if count > 0 && !collector.consume(buffer[:count]) {
			_ = cmd.Process.Kill()
			_ = stdout.Close()
			break
		}
		if currentErr != nil {
			if !errors.Is(currentErr, io.EOF) {
				readErr = currentErr
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if collector.truncated {
		collector.commitPendingNewlines()
		return collector.output, true, nil
	}
	if readErr != nil {
		return nil, false, readErr
	}
	if waitErr == nil {
		return collector.output, false, nil
	}
	var exitErr *exec.ExitError
	if allowExitOne && errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
		return collector.output, false, nil
	}
	detail := strings.TrimSpace(string(stderr.data))
	if detail != "" {
		return nil, false, fmt.Errorf("%w: %s", waitErr, detail)
	}
	return nil, false, waitErr
}

func (r *View) changedPatch(
	base, head string,
	files []string,
	maxLines int,
) (string, bool, error) {
	if len(files) == 0 {
		return "", false, nil
	}
	patch := newPatchOutputCollector(maxLines, maximumPatchBytes)
	staged := []string{
		"diff",
		"--cached",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--ignore-submodules=dirty",
		"--find-renames",
	}
	if head != "" {
		staged = append(staged, head)
	}
	commands := [][]string{staged, {
		"diff",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--ignore-submodules=dirty",
		"--find-renames",
	}}
	if base != "" {
		commands = [][]string{gitdiffcontract.PatchArguments(base, head)}
	}
	for _, args := range commands {
		if base == "" {
			args = append(args, "--")
		}
		args = append(args, files...)
		cmd := r.gitCommand(args...)
		output, truncated, err := boundedPatchCommandOutput(
			cmd,
			maxLines,
			maximumPatchBytes,
			false,
		)
		if err != nil {
			return "", false, fmt.Errorf("read changed patch: %w", err)
		}
		appended := patch.appendFragment(output)
		if truncated {
			patch.commitPendingNewlines()
		}
		if truncated || !appended {
			return string(patch.output), true, nil
		}
	}
	if base == "" {
		args := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}
		args = append(args, files...)
		untracked, err := r.gitFileList(args...)
		if err != nil {
			return "", false, fmt.Errorf("list untracked changed files: %w", err)
		}
		gitDir := ""
		if len(untracked) > 0 {
			gitDir, err = r.absoluteGitDir()
			if err != nil {
				return "", false, err
			}
		}
		for _, rel := range untracked {
			clean, resolveErr := r.validateRelativeRegularFile(rel)
			if resolveErr != nil {
				continue
			}
			output, truncated, diffErr := r.untrackedFilePatch(
				clean,
				gitDir,
				maxLines,
				maximumPatchBytes,
			)
			if errors.Is(diffErr, errSnapshotBudget) {
				return string(patch.output), true, nil
			}
			if diffErr != nil {
				return "", false, fmt.Errorf("read untracked patch for %s: %w", rel, diffErr)
			}
			appended := patch.appendFragment(output)
			if truncated {
				patch.commitPendingNewlines()
			}
			if truncated || !appended {
				return string(patch.output), true, nil
			}
		}
	}
	return string(patch.output), false, nil
}

func (r *View) untrackedFilePatch(
	rel, gitDir string,
	maxLines, maxBytes int,
) ([]byte, bool, error) {
	snapshotRoot, err := os.MkdirTemp("", "scopesifter-untracked-patch-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(snapshotRoot)
	snapshotBytesRemaining := int64(maxBytes)

	snapshotPath := filepath.Join(snapshotRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o700); err != nil {
		return nil, false, err
	}
	if err := r.snapshotRelativeRegularFile(
		rel,
		snapshotPath,
		&snapshotBytesRemaining,
	); err != nil {
		return nil, false, err
	}
	if err := r.snapshotGitAttributes(
		rel,
		snapshotRoot,
		&snapshotBytesRemaining,
	); err != nil {
		return nil, false, err
	}
	cmd := r.gitCommand(
		"--git-dir="+gitDir,
		"--work-tree="+snapshotRoot,
		"diff",
		"--no-index",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--",
		os.DevNull,
		rel,
	)
	cmd.Dir = snapshotRoot
	return boundedPatchCommandOutput(cmd, maxLines, maxBytes, true)
}

func (r *View) snapshotGitAttributes(
	rel, snapshotRoot string,
	snapshotBytesRemaining *int64,
) error {
	directories := []string{""}
	if directory := pathpkg.Dir(rel); directory != "." {
		current := ""
		for _, component := range strings.Split(directory, "/") {
			current = pathpkg.Join(current, component)
			directories = append(directories, current)
		}
	}
	for _, directory := range directories {
		attributePath := pathpkg.Join(directory, ".gitattributes")
		if attributePath == rel {
			continue
		}
		clean, err := r.validateRelativeRegularFile(attributePath)
		if err != nil {
			continue
		}
		snapshotPath := filepath.Join(snapshotRoot, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o700); err != nil {
			return err
		}
		if err := r.snapshotRelativeRegularFile(
			clean,
			snapshotPath,
			snapshotBytesRemaining,
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *View) absoluteGitDir() (string, error) {
	cmd := r.gitCommand("rev-parse", "--absolute-git-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git metadata directory: %w", err)
	}
	directory := strings.TrimSuffix(string(output), "\n")
	if directory == "" || strings.ContainsRune(directory, '\x00') {
		return "", fmt.Errorf("git metadata directory is invalid")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(r.root, directory)
	}
	return filepath.Clean(directory), nil
}

func (r *View) gitFileList(args ...string) ([]string, error) {
	output, err := r.gitOutput(args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range strings.Split(string(output), "\x00") {
		if name != "" {
			files = append(files, filepath.ToSlash(name))
		}
	}
	return files, nil
}

func (r *View) resolveBase(base string) (string, error) {
	if base == "" {
		return "", nil
	}
	if strings.HasPrefix(base, "-") ||
		strings.ContainsAny(base, "\x00\n\r") {
		return "", fmt.Errorf("invalid Git base revision %q", base)
	}
	output, err := r.gitOutput(
		"rev-parse",
		"--verify",
		"--end-of-options",
		base+"^{commit}",
	)
	if err != nil {
		return "", fmt.Errorf("resolve Git base revision %q: %w", base, err)
	}
	resolved := strings.TrimSpace(string(output))
	if !regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(resolved) {
		return "", fmt.Errorf("git base revision %q did not resolve canonically", base)
	}
	return resolved, nil
}

// VerifyBaseCommit verifies that base resolves canonically and is an existing
// ancestor of HEAD.
func (r *View) VerifyBaseCommit(base string) error {
	resolved, err := r.resolveBase(base)
	if err != nil {
		return err
	}
	if resolved != base {
		return fmt.Errorf("git base commit resolved to %s, want %s", resolved, base)
	}
	if _, err := r.gitOutput("merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return fmt.Errorf("git base commit is not an ancestor of HEAD: %w", err)
	}
	return nil
}

func (r *View) resolveOptionalHead() (string, error) {
	output, err := r.gitOutput(
		"rev-parse",
		"--verify",
		"--quiet",
		"--end-of-options",
		"HEAD^{commit}",
	)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("resolve Git HEAD commit: %w", err)
	}
	resolved := strings.TrimSpace(string(output))
	if !canonicalGitObjectID(resolved) {
		return "", fmt.Errorf("git HEAD did not resolve canonically")
	}
	return resolved, nil
}

func (r *View) gitText(args ...string) (string, error) {
	output, err := r.gitOutput(args...)
	return strings.TrimSpace(string(output)), err
}

func (r *View) changedLines(
	rel, base, head string,
	sourceLineCount int,
) ([]int, error) {
	if base == "" && head == "" {
		if sourceLineCount > maximumExpandedChangedLines {
			return nil, errors.New("changed-line expansion exceeds its limit")
		}
		return integerRange(1, sourceLineCount), nil
	}
	var args []string
	if base != "" {
		args = gitdiffcontract.ChangedLineArguments(base, head)
	} else {
		args = []string{
			"diff",
			"--no-color",
			"--no-ext-diff",
			"--no-textconv",
			"--ignore-submodules=dirty",
			"--unified=0",
		}
		// Compare HEAD directly with the working tree so staged and unstaged
		// changes share the working tree's line-coordinate system.
		args = append(args, head)
	}
	if base != "" {
		args = append(args, rel)
	} else {
		args = append(args, "--", rel)
	}
	cmd := r.gitCommand(args...)
	lines, err := changedLineNumbersFromCommand(cmd, sourceLineCount)
	if err != nil {
		return nil, fmt.Errorf("read changed lines for %s: %w", rel, err)
	}
	if base == "" && len(lines) == 0 {
		untracked, listErr := r.gitFileList(
			"ls-files", "--others", "--exclude-standard", "-z", "--", rel,
		)
		if listErr != nil {
			return nil, fmt.Errorf("inspect untracked source %s: %w", rel, listErr)
		}
		for _, candidate := range untracked {
			if candidate != rel {
				continue
			}
			if sourceLineCount > maximumExpandedChangedLines {
				return nil, errors.New("changed-line expansion exceeds its limit")
			}
			return integerRange(1, sourceLineCount), nil
		}
	}
	sort.Ints(lines)
	return uniqueInts(lines), nil
}

func changedLineNumbersFromCommand(
	cmd *exec.Cmd,
	maximumLine int,
) ([]int, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &boundedByteWriter{limit: maximumGitStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	reader := bufio.NewReaderSize(stdout, 32<<10)
	header := make([]byte, 0, maximumHunkHeaderBytes)
	lines := make([]int, 0)
	var readErr error
	for {
		header = header[:0]
		finished := false
		for {
			fragment, isPrefix, currentErr := reader.ReadLine()
			if len(header) < maximumHunkHeaderBytes {
				remaining := maximumHunkHeaderBytes - len(header)
				header = append(header, fragment[:min(len(fragment), remaining)]...)
			}
			if currentErr != nil {
				finished = true
				if !errors.Is(currentErr, io.EOF) {
					readErr = currentErr
				}
				break
			}
			if !isPrefix {
				break
			}
		}
		if len(header) >= 3 && header[0] == '@' && header[1] == '@' && header[2] == ' ' {
			start, end, ok := changedHunkLineRange(string(header), maximumLine)
			if ok {
				width := end - start + 1
				if width > maximumExpandedChangedLines-len(lines) {
					readErr = errors.New("changed-line expansion exceeds its limit")
					finished = true
				} else {
					for line := start; line <= end; line++ {
						lines = append(lines, line)
					}
				}
			}
		}
		if finished {
			break
		}
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(string(stderr.data))
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", waitErr, detail)
		}
		return nil, waitErr
	}
	return lines, nil
}

func integerRange(start, end int) []int {
	if end < start {
		return nil
	}
	values := make([]int, end-start+1)
	for index := range values {
		values[index] = start + index
	}
	return values
}

func (r *View) gitCommand(args ...string) *exec.Cmd {
	ctx := r.operationContext()

	failedCommand := func(err error) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git")
		cmd.Err = err
		return cmd
	}
	if err := r.verifyRootIdentity(); err != nil {
		return failedCommand(err)
	}
	if err := r.validateGitWorktreeConfiguration(ctx); err != nil {
		return failedCommand(err)
	}
	safeArgs := gitdiffcontract.InvocationPrefix()
	safeArgs = append(safeArgs, args...)
	if err := processpolicy.ValidateGit(safeArgs...); err != nil {
		return failedCommand(fmt.Errorf("reject Git invocation: %w", err))
	}

	var cmd *exec.Cmd
	if r.pinnedGit == nil {
		nativeCommand, gitFile, err := processpolicy.NativeCommandContext(ctx, "git", safeArgs...)
		if err != nil {
			return failedCommand(fmt.Errorf("pin native Git: %w", err))
		}
		cmd = nativeCommand
		runtime.SetFinalizer(cmd, func(*exec.Cmd) {
			_ = gitFile.Close()
		})
	} else {
		pinnedCommand, executable, err := r.pinnedGit.commandContext(ctx, safeArgs...)
		if err != nil {
			return failedCommand(fmt.Errorf("pin git executable for invocation: %w", err))
		}
		cmd = pinnedCommand
		// Cmd has no completion callback. Keep the authenticated descriptor alive
		// through Start/Wait and close it when the completed command becomes
		// unreachable. Existing bounded gitOutput calls retain their stronger
		// immediate close and post-execution identity verification.
		runtime.SetFinalizer(cmd, func(*exec.Cmd) {
			_ = executable.Close()
		})
	}
	cmd.Dir = r.root
	cmd.Env = isolatedGitEnvironment()
	return cmd
}

func (r *View) gitOutput(args ...string) ([]byte, error) {
	return r.gitOutputContext(r.operationContext(), args...)
}

func (r *View) gitOutputContext(
	ctx context.Context,
	args ...string,
) ([]byte, error) {
	return r.gitOutputContextLimit(ctx, maximumGitOutputBytes, args...)
}

func (r *View) gitOutputContextLimit(
	ctx context.Context,
	limit int,
	args ...string,
) (output []byte, resultErr error) {
	if err := r.validateGitWorktreeConfiguration(ctx); err != nil {
		return nil, err
	}
	return r.gitOutputContextLimitRaw(ctx, limit, args...)
}

func (r *View) validateGitWorktreeConfiguration(ctx context.Context) error {
	output, err := r.gitOutputContextLimitRaw(
		ctx, 1<<20, processpolicy.GitRepositoryConfigArguments()...,
	)
	if err != nil {
		return fmt.Errorf("inspect repository Git configuration: %w", err)
	}
	if err := processpolicy.ValidateGitWorktreeConfig(output); err != nil {
		return fmt.Errorf("reject repository Git configuration: %w", err)
	}
	return nil
}

func (r *View) gitOutputContextLimitRaw(
	ctx context.Context,
	limit int,
	args ...string,
) (output []byte, resultErr error) {
	if limit <= 0 || limit > maximumGitOutputBytes {
		return nil, errors.New("git output limit is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	safeArgs := args
	if !processpolicy.IsGitRepositoryConfigQuery(args) {
		safeArgs = append(gitdiffcontract.InvocationPrefix(), args...)
	}
	if err := processpolicy.ValidateGit(safeArgs...); err != nil {
		return nil, fmt.Errorf("reject Git invocation: %w", err)
	}
	var (
		cmd       *exec.Cmd
		pinnedGit *os.File
		err       error
	)
	if r.pinnedGit == nil {
		cmd, pinnedGit, err = processpolicy.NativeCommandContext(ctx, "git", safeArgs...)
		if err != nil {
			return nil, fmt.Errorf("pin native Git: %w", err)
		}
	} else {
		cmd, pinnedGit, err = r.pinnedGit.commandContext(ctx, safeArgs...)
		if err != nil {
			return nil, fmt.Errorf("pin git executable for invocation: %w", err)
		}
	}
	defer func() {
		if closeErr := pinnedGit.Close(); closeErr != nil {
			resultErr = &gitExecutableCloseError{
				operation: resultErr,
				close:     fmt.Errorf("close pinned git executable: %w", closeErr),
			}
			output = nil
		}
	}()
	cmd.Dir = r.root
	cmd.Env = isolatedGitEnvironment()
	stdout := &boundedOutputBuffer{limit: limit}
	stderr := &boundedOutputBuffer{limit: maximumGitErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	commandErr := cmd.Run()
	output = append([]byte(nil), stdout.Bytes()...)
	if r.pinnedGit != nil {
		if err := r.pinnedGit.verify(); err != nil {
			return nil, fmt.Errorf("verify git executable after invocation: %w", err)
		}
	}
	if commandErr != nil {
		if stderr.Len() != 0 {
			return output, fmt.Errorf(
				"git %s: %w: %s",
				strings.Join(args, " "),
				commandErr,
				bytes.TrimSpace(stderr.Bytes()),
			)
		}
		return output, fmt.Errorf(
			"git %s: %w",
			strings.Join(args, " "),
			commandErr,
		)
	}
	return output, nil
}

func isolatedGitEnvironment() []string {
	environment := gitdiffcontract.Environment(os.DevNull)
	if runtime.GOOS == "windows" {
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			environment = append(environment, "SystemRoot="+systemRoot)
		}
	}
	return environment
}

func parseCanonicalChangedLineNumbers(patch []byte) ([]int, error) {
	spans, err := gitdiffcontract.ParseChangedSpans(
		patch,
		maximumChangedSpansPerFile,
		maximumChangedLine,
	)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, span := range spans {
		width := span.End - span.Start + 1
		if width > maximumExpandedChangedLines-count {
			return nil, errors.New("changed-line expansion exceeds its limit")
		}
		count += width
	}
	lines := make([]int, 0, count)
	for _, span := range spans {
		for lineNo := span.Start; lineNo <= span.End; lineNo++ {
			lines = append(lines, lineNo)
		}
	}
	return lines, nil
}

func parseChangedLineNumbers(patch string) []int {
	lines, _ := parseCanonicalChangedLineNumbers([]byte(patch))
	return lines
}

func changedHunkLineRange(header string, maximumLine int) (int, int, bool) {
	match := changedHunkPattern.FindStringSubmatch(header)
	if match == nil || maximumLine < 1 {
		return 0, 0, false
	}
	start, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	count := 1
	if match[2] != "" {
		count, err = strconv.Atoi(match[2])
		if err != nil {
			return 0, 0, false
		}
	}
	start = min(max(1, start), maximumLine)
	if count == 0 {
		return start, start, true
	}
	remaining := maximumLine - start
	if count-1 > remaining {
		return start, maximumLine, true
	}
	return start, start + count - 1, true
}

func uniqueInts(values []int) []int {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
