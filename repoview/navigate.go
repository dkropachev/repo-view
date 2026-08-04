package repoview

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

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

type Options struct {
	Return         Return
	Include        Include
	Base           string
	PathGlobs      []string
	ExcludeGlobs   []string
	MaxCodeLines   int
	Context        int
	Limit          int
	MaxPatchLines  int
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

type NavigationBudget struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

type FindResponse struct {
	NavigationBudget *NavigationBudget `json:"navigation_budget,omitempty"`
	Symbol           string            `json:"symbol"`
	Root             string            `json:"root"`
	Results          []Result          `json:"results"`
	ResultsTruncated bool              `json:"results_truncated"`
}

type InspectResponse struct {
	NavigationBudget *NavigationBudget `json:"navigation_budget,omitempty"`
	Location         string            `json:"location"`
	Root             string            `json:"root"`
	Symbol           string            `json:"symbol,omitempty"`
	Error            string            `json:"error,omitempty"`
	Results          []Result          `json:"results"`
	ResultsTruncated bool              `json:"results_truncated"`
}

type OutlineResponse struct {
	NavigationBudget *NavigationBudget `json:"navigation_budget,omitempty"`
	Path             string            `json:"path"`
	Root             string            `json:"root"`
	Error            string            `json:"error,omitempty"`
	Results          []Result          `json:"results"`
	ResultsTruncated bool              `json:"results_truncated"`
}

type ChangedResponse struct {
	NavigationBudget *NavigationBudget `json:"navigation_budget,omitempty"`
	Root             string            `json:"root"`
	Base             string            `json:"base,omitempty"`
	BaseCommit       string            `json:"base_commit,omitempty"`
	HeadCommit       string            `json:"head_commit"`
	HeadSubject      string            `json:"head_subject,omitempty"`
	Patch            string            `json:"patch,omitempty"`
	Results          []Result          `json:"results"`
	PatchTruncated   bool              `json:"patch_truncated"`
	ResultsTruncated bool              `json:"results_truncated"`
}

func (r *RepoView) Find(symbol string, opts Options) (FindResponse, error) {
	responses, err := r.FindMany([]string{symbol}, opts)
	if err != nil {
		return FindResponse{}, err
	}
	return responses[0], nil
}

// FindMany searches for several symbols in one pass over the selected files.
// Limit is shared fairly across the supplied symbols, matching the CLI's
// batched find behavior.
func (r *RepoView) FindMany(symbols []string, opts Options) ([]FindResponse, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("at least one symbol is required")
	}
	for _, symbol := range symbols {
		if symbol == "" {
			return nil, fmt.Errorf("symbol must not be empty")
		}
	}
	opts = normalizeOptions(opts, ReturnScope)
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	files, err := r.filteredFiles(opts)
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
	states := make([]symbolState, len(symbols))
	for index, symbol := range symbols {
		states[index] = symbolState{
			symbol:  symbol,
			results: make([]Result, 0),
			seen:    make(map[string]bool),
		}
	}

	for _, path := range files {
		rel, _ := filepath.Rel(r.root, path)
		rel = filepath.ToSlash(rel)
		if opts.ChangedOnly && !changed[rel] {
			continue
		}
		lines, err := readLines(path)
		if err != nil {
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
		processStateLine := func(
			state *symbolState,
			lineOccurrenceCount func(string) int,
			lineNo, occurrenceAdjustment int,
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
				result := r.resultForFindHit(
					state.symbol,
					kindForMatch(isDefinition),
					rel,
					language,
					lines,
					lineNo,
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
			state := &states[stateIndex]
			if opts.Limit > 0 && len(state.results) > opts.Limit {
				continue
			}
			lineOccurrenceCount := func(line string) int {
				return countSymbolOccurrences(line, state.symbol)
			}
			if preparer, ok := language.(symbolOccurrenceCounterPreparer); ok {
				lineOccurrenceCount = preparer.prepareSymbolOccurrenceCounter(state.symbol)
			} else if counter, ok := language.(symbolOccurrenceCounter); ok {
				lineOccurrenceCount = func(line string) int {
					return counter.countSymbolOccurrences(line, state.symbol)
				}
			}
			if walker, ok := language.(sourceSymbolOccurrenceAugmenter); ok &&
				walker.walkAdditionalSymbolOccurrences(
					lines, state.symbol,
					func(lineNo, occurrenceAdjustment int) bool {
						return processStateLine(
							state, lineOccurrenceCount,
							lineNo, occurrenceAdjustment,
						)
					},
				) {
				continue
			}
			for lineIndex := range searchLines {
				if !processStateLine(state, lineOccurrenceCount, lineIndex+1, 0) {
					break
				}
			}
		}
	}

	remaining := opts.Limit
	responses := make([]FindResponse, 0, len(states))
	for index, state := range states {
		resultLimit := len(state.results)
		if opts.Limit > 0 {
			resultLimit = fairResultLimit(remaining, len(states)-index)
			if resultLimit > len(state.results) {
				resultLimit = len(state.results)
			}
		}
		response := FindResponse{
			Symbol:           state.symbol,
			Root:             r.root,
			Results:          state.results[:resultLimit],
			ResultsTruncated: len(state.results) > resultLimit,
		}
		responses = append(responses, response)
		if remaining > 0 {
			remaining -= resultLimit
			if remaining <= 0 {
				break
			}
		}
	}
	return responses, nil
}

func fairResultLimit(remaining, remainingSelectors int) int {
	limit := remaining / remainingSelectors
	if remaining%remainingSelectors != 0 {
		limit++
	}
	return limit
}

func (r *RepoView) Inspect(location string, opts Options) (InspectResponse, error) {
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
	if lineNo < 1 || lineNo > len(lines) {
		return InspectResponse{}, fmt.Errorf("line %d out of range for %s", lineNo, path)
	}
	language := prepareLanguageBackend(languageForExtension(filepath.Ext(path)), lines)
	symbol := bestSymbolOnLine(lines, lineNo, language)
	results := []Result{
		r.resultForHit(symbol, "scope", filepath.ToSlash(path), language, lines, lineNo, opts),
	}
	if opts.Include == IncludeImports || opts.Include == IncludeAll {
		if result, ok := importResult(filepath.ToSlash(path), language, lines); ok {
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
		}
		if relatedLimit <= 0 && opts.Limit > 0 {
			return InspectResponse{
				Location:         location,
				Root:             r.root,
				Symbol:           symbol,
				Results:          results[:opts.Limit],
				ResultsTruncated: true,
			}, nil
		}
		related, err := r.Find(symbol, Options{
			Include:        relatedInclude,
			Return:         opts.Return,
			Context:        opts.Context,
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

func (r *RepoView) Outline(path string, opts Options) (OutlineResponse, error) {
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

func (r *RepoView) Changed(opts Options) (ChangedResponse, error) {
	opts = normalizeOptions(opts, ReturnContext)
	if err := validateOptions(opts); err != nil {
		return ChangedResponse{}, err
	}
	baseCommit, err := r.resolveBase(opts.Base)
	if err != nil {
		return ChangedResponse{}, err
	}
	files, err := r.changedFiles(baseCommit)
	if err != nil {
		return ChangedResponse{}, err
	}
	response := ChangedResponse{
		Root:        r.root,
		Base:        opts.Base,
		BaseCommit:  baseCommit,
		HeadCommit:  r.gitOutput("rev-parse", "HEAD"),
		HeadSubject: r.gitOutput("show", "-s", "--format=%s", "HEAD"),
	}
	var selectedFiles []string
	for _, rel := range files {
		if matchPathFilters(rel, opts.PathGlobs, opts.ExcludeGlobs) {
			selectedFiles = append(selectedFiles, rel)
		}
	}
	patch, patchTruncated, err := r.changedPatch(
		baseCommit,
		selectedFiles,
		opts.MaxPatchLines,
	)
	if err != nil {
		return ChangedResponse{}, err
	}
	results := make([]Result, 0)
	for _, rel := range selectedFiles {
		language := languageForExtension(filepath.Ext(rel))
		lines, cleanRel, err := r.readRelativeLines(rel)
		if err != nil || len(lines) == 0 {
			results = append(results, Result{Kind: "file", Path: rel, Language: language.name()})
			continue
		}
		language = prepareLanguageBackend(language, lines)
		rel = cleanRel
		lineNumbers, err := r.changedLines(rel, baseCommit)
		if err != nil {
			return ChangedResponse{}, err
		}
		if len(lineNumbers) == 0 {
			lineNumbers = []int{1}
		}
		if opts.Return == ReturnLocations {
			for _, span := range mergeContextRanges(len(lines), lineNumbers, 0) {
				results = append(results, r.resultForRange(
					"", "changed", rel, language, lines, span[0], span[1],
					linesInRange(lineNumbers, span[0], span[1]), opts,
				))
			}
			continue
		}
		if opts.Return == ReturnContext {
			for _, span := range mergeContextRanges(len(lines), lineNumbers, opts.Context) {
				results = append(results, r.resultForRange(
					"", "changed", rel, language, lines, span[0], span[1],
					linesInRange(lineNumbers, span[0], span[1]), opts,
				))
			}
			continue
		}
		for _, lineNo := range lineNumbers {
			if lineNo < 1 || lineNo > len(lines) {
				lineNo = 1
			}
			results = append(results, r.resultForHit("", "changed", rel, language, lines, lineNo, opts))
			results = dedupeResults(results)
		}
	}
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
		response.ResultsTruncated = true
	}
	response.Patch = patch
	response.PatchTruncated = patchTruncated
	response.Results = results
	return response, nil
}

func (r *RepoView) resultForRange(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	start, end int,
	hitLines []int,
	opts Options,
) Result {
	lineNo := start
	result := Result{
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

func (r *RepoView) resultForHit(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	lineNo int,
	opts Options,
) Result {
	return r.resultForFindHit(
		symbol, kind, rel, language, lines, lineNo, opts, nil, nil,
	)
}

func (r *RepoView) resultForFindHit(
	symbol, kind, rel string,
	language languageBackend,
	lines []string,
	lineNo int,
	opts Options,
	findScopeResolver preparedFindScopeResolver,
	preparedSnippet *preparedFindSnippet,
) Result {
	start, end := lineNo, lineNo
	switch opts.Return {
	case ReturnScope:
		if findScopeResolver != nil {
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
	case findScopeResolver != nil:
		scope = findScopeResolver.scopeName(lineNo)
	default:
		scope = scopeName(lines, lineNo, language)
	}
	result := Result{
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
	if opts.Include == "" {
		opts.Include = IncludeBoth
	}
	if opts.Return == "" {
		opts.Return = defaultReturn
	}
	if opts.Context == 0 {
		opts.Context = 5
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

func (r *RepoView) filteredFiles(opts Options) ([]string, error) {
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

func importResult(path string, language languageBackend, lines []string) (Result, bool) {
	start, end, ok := language.importRange(lines)
	if !ok {
		return Result{}, false
	}
	return Result{
		Kind:      "imports",
		Path:      path,
		StartLine: start,
		EndLine:   end,
		Language:  language.name(),
		Code:      strings.Join(lines[start-1:end], "\n"),
	}, true
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

func (r *RepoView) changedFileSet(opts Options) (map[string]bool, error) {
	baseCommit, err := r.resolveBase(opts.Base)
	if err != nil {
		return nil, err
	}
	files, err := r.changedFiles(baseCommit)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, file := range files {
		out[file] = true
	}
	return out, nil
}

func (r *RepoView) changedFiles(base string) ([]string, error) {
	if base != "" {
		return r.gitFileList(
			"diff",
			"--no-ext-diff",
			"--no-textconv",
			"--name-only",
			"-z",
			base+"...HEAD",
		)
	}
	commands := [][]string{
		{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z"},
		{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--name-only", "-z"},
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

func (r *RepoView) changedPatch(
	base string,
	files []string,
	maxLines int,
) (string, bool, error) {
	if len(files) == 0 {
		return "", false, nil
	}
	var outputs []string
	commands := [][]string{{
		"diff",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--find-renames",
	}}
	if base != "" {
		commands = [][]string{{
			"diff",
			"--no-color",
			"--no-ext-diff",
			"--no-textconv",
			"--find-renames",
			base + "...HEAD",
		}}
	} else {
		commands = append(commands, []string{
			"diff",
			"--cached",
			"--no-color",
			"--no-ext-diff",
			"--no-textconv",
			"--find-renames",
		})
	}
	for _, args := range commands {
		args = append(args, "--")
		args = append(args, files...)
		cmd := r.gitCommand(args...)
		output, err := cmd.Output()
		if err != nil {
			return "", false, fmt.Errorf("read changed patch: %w", err)
		}
		if len(output) > 0 {
			outputs = append(outputs, strings.TrimRight(string(output), "\n"))
		}
	}
	if base == "" {
		args := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}
		args = append(args, files...)
		untracked, err := r.gitFileList(args...)
		if err != nil {
			return "", false, fmt.Errorf("list untracked changed files: %w", err)
		}
		for _, rel := range untracked {
			if _, _, err := r.resolveRegularPath(rel); err != nil {
				continue
			}
			cmd := r.gitCommand(
				"diff",
				"--no-index",
				"--no-color",
				"--no-ext-diff",
				"--no-textconv",
				"--",
				os.DevNull,
				rel,
			)
			output, diffErr := cmd.Output()
			var exitErr *exec.ExitError
			if diffErr != nil &&
				(!errors.As(diffErr, &exitErr) || exitErr.ExitCode() != 1) {
				return "", false, fmt.Errorf("read untracked patch for %s: %w", rel, diffErr)
			}
			if len(output) > 0 {
				outputs = append(outputs, strings.TrimRight(string(output), "\n"))
			}
		}
	}
	patch := strings.Join(outputs, "\n")
	lines := strings.Split(patch, "\n")
	if maxLines > 0 && len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n"), true, nil
	}
	return patch, false, nil
}

func (r *RepoView) gitFileList(args ...string) ([]string, error) {
	cmd := r.gitCommand(args...)
	output, err := cmd.Output()
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

func (r *RepoView) resolveBase(base string) (string, error) {
	if base == "" {
		return "", nil
	}
	if strings.HasPrefix(base, "-") ||
		strings.ContainsAny(base, "\x00\n\r") {
		return "", fmt.Errorf("invalid Git base revision %q", base)
	}
	cmd := r.gitCommand(
		"rev-parse",
		"--verify",
		"--end-of-options",
		base+"^{commit}",
	)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git base revision %q: %w", base, err)
	}
	resolved := strings.TrimSpace(string(output))
	if !regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(resolved) {
		return "", fmt.Errorf("git base revision %q did not resolve canonically", base)
	}
	return resolved, nil
}

func (r *RepoView) gitOutput(args ...string) string {
	cmd := r.gitCommand(args...)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *RepoView) changedLines(rel, base string) ([]int, error) {
	args := []string{
		"diff",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		"--unified=0",
	}
	if base != "" {
		args = append(args, base+"...HEAD")
	}
	args = append(args, "--", rel)
	cmd := r.gitCommand(args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read changed lines for %s: %w", rel, err)
	}
	lines := parseChangedLineNumbers(string(output))
	if base == "" {
		cachedArgs := []string{
			"diff",
			"--cached",
			"--no-color",
			"--no-ext-diff",
			"--no-textconv",
			"--unified=0",
			"--",
			rel,
		}
		cached := r.gitCommand(cachedArgs...)
		cachedOutput, err := cached.Output()
		if err != nil {
			return nil, fmt.Errorf("read staged changed lines for %s: %w", rel, err)
		}
		lines = append(lines, parseChangedLineNumbers(string(cachedOutput))...)
	}
	sort.Ints(lines)
	return uniqueInts(lines), nil
}

func (r *RepoView) gitCommand(args ...string) *exec.Cmd {
	safeArgs := []string{
		"-c", "color.ui=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "core.untrackedCache=false",
		"-c", "diff.external=",
	}
	safeArgs = append(safeArgs, args...)
	cmd := exec.Command("git", safeArgs...)
	cmd.Dir = r.root
	cmd.Env = isolatedGitEnvironment()
	return cmd
}

func isolatedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func parseChangedLineNumbers(patch string) []int {
	re := regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
	var lines []int
	for _, match := range re.FindAllStringSubmatch(patch, -1) {
		start, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		count := 1
		if match[2] != "" {
			count, err = strconv.Atoi(match[2])
			if err != nil {
				continue
			}
		}
		if count == 0 {
			lines = append(lines, max(1, start))
			continue
		}
		for lineNo := start; lineNo < start+count; lineNo++ {
			lines = append(lines, lineNo)
		}
	}
	return lines
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
