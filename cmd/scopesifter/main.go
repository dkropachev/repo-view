package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yapless/scopesifter/navigator"
)

var (
	releaseRevision       = "development"
	releaseRevisionMarker = "scopesifter.release-revision=development"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		printUsage(os.Stdout)
		return 0
	case "help":
		if len(args) == 1 {
			printUsage(os.Stdout)
			return 0
		}
		return run(append([]string{args[1], "--help"}, args[2:]...))
	case "find":
		return runFind(args[1:])
	case "inspect":
		return runInspect(args[1:])
	case "outline":
		return runOutline(args[1:])
	case "changed":
		return runChanged(args[1:])
	case "version":
		if releaseRevisionMarker != "scopesifter.release-revision="+releaseRevision {
			fmt.Fprintln(os.Stderr, "scopesifter: release revision marker is invalid")
			return 1
		}
		fmt.Fprintln(os.Stdout, releaseRevision)
		return 0
	default:
		printUsage(os.Stderr)
		return 2
	}
}

func runFind(args []string) int {
	flags := flag.NewFlagSet("find", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "scopesifter find QUERY...", "Locate exact source identifiers or literal repository paths. Each response reports which kind matched.")
		flags.PrintDefaults()
	}
	include := flags.String("include", "both", "defs, refs, or both")
	match := flags.String("match", "auto", "auto, symbol, or path")
	if showCommandHelp(flags, args) {
		return 0
	}
	queries, flagArgs, ok := splitPositionals(args)
	if !ok {
		flags.Usage()
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument:", safeOutputText(flags.Arg(0)))
		return 2
	}
	if err := validateInclude("find", navigator.Include(*include), navigator.IncludeDefs, navigator.IncludeRefs, navigator.IncludeBoth); err != nil {
		printCLIError(err)
		return 2
	}
	view, err := navigator.New(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	options, err := common.buildOptions(navigator.Include(*include))
	if err != nil {
		printCLIError(err)
		return 2
	}
	options.Match = navigator.FindMatch(*match)
	switch options.Match {
	case navigator.FindMatchAuto, navigator.FindMatchSymbol, navigator.FindMatchPath:
	default:
		fmt.Fprintln(os.Stderr, "find --match must be one of: auto, symbol, path")
		return 2
	}
	if options.Limit > 0 && options.Limit < len(queries) {
		fmt.Fprintf(
			os.Stderr,
			"find --limit %d is smaller than %d requested queries; use at least --limit %d\n",
			options.Limit,
			len(queries),
			len(queries),
		)
		return 2
	}
	responses, err := view.FindMany(queries, options)
	if err != nil {
		printCLIError(err)
		return 1
	}
	if *common.jsonOut {
		return printJSON(responses, *common.prettyJSON)
	}
	for _, response := range responses {
		printFindResponse(response, options.Return)
	}
	return 0
}

func printFindResponse(response navigator.FindResponse, returnMode navigator.Return) {
	if len(response.Results) == 0 {
		fmt.Printf("# %s: %s\n", safeOutputText(response.Query), response.MatchedAs)
		if response.Hint != "" {
			fmt.Printf("# %s\n", safeOutputText(response.Hint))
		}
		return
	}
	fmt.Printf("# %s: %s\n", safeOutputText(response.Query), response.MatchedAs)
	printResults(response.Results, returnMode)
}

func fairResultLimit(remaining, remainingSymbols int) int {
	return (remaining + remainingSymbols - 1) / remainingSymbols
}

func runInspect(args []string) int {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "scopesifter inspect PATH:LINE...", "Return the enclosing scope and optionally imports or related results for one or more source locations.")
		flags.PrintDefaults()
	}
	include := flags.String("include", "scope", "symbol, scope, defs, refs, both, imports, or all")
	if showCommandHelp(flags, args) {
		return 0
	}
	locations, flagArgs, ok := splitPositionals(args)
	if !ok {
		flags.Usage()
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	if err := validateInclude(
		"inspect",
		navigator.Include(*include),
		navigator.IncludeSymbol,
		navigator.IncludeScope,
		navigator.IncludeDefs,
		navigator.IncludeRefs,
		navigator.IncludeBoth,
		navigator.IncludeImports,
		navigator.IncludeAll,
	); err != nil {
		printCLIError(err)
		return 2
	}
	view, err := navigator.New(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	absoluteRoot, err := filepath.Abs(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	options, err := common.buildOptions(navigator.Include(*include))
	if err != nil {
		printCLIError(err)
		return 2
	}
	if options.Limit > 0 && options.Limit < len(locations) {
		fmt.Fprintf(
			os.Stderr,
			"inspect --limit %d is smaller than %d requested locations; use at least --limit %d\n",
			options.Limit,
			len(locations),
			len(locations),
		)
		return 2
	}
	remaining := options.Limit
	responses := make([]navigator.InspectResponse, 0, len(locations))
	successCount := 0
	for index, location := range locations {
		locationOptions := options
		if options.Limit > 0 {
			remainingLocations := len(locations) - index
			locationOptions.Limit = fairResultLimit(remaining, remainingLocations)
		}
		response, err := view.Inspect(location, locationOptions)
		if err != nil {
			if len(locations) == 1 {
				printCLIError(err)
				return 1
			}
			responses = append(responses, navigator.InspectResponse{
				Location: location,
				Root:     absoluteRoot,
				Results:  []navigator.Result{},
				Error:    err.Error(),
			})
			continue
		}
		successCount++
		responses = append(responses, response)
		if remaining > 0 {
			remaining -= len(response.Results)
		}
	}
	if *common.jsonOut {
		if len(responses) == 1 {
			return printJSON(responses[0], *common.prettyJSON)
		}
		if status := printJSON(responses, *common.prettyJSON); status != 0 {
			return status
		}
		if successCount == 0 {
			return 1
		}
		return 0
	}
	for _, response := range responses {
		if response.Error != "" {
			fmt.Fprintf(
				os.Stderr,
				"%s: %s\n",
				safeOutputText(response.Location),
				safeOutputText(response.Error),
			)
			continue
		}
		printResults(response.Results, options.Return)
	}
	if successCount == 0 {
		return 1
	}
	return 0
}

func runOutline(args []string) int {
	flags := flag.NewFlagSet("outline", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnLine)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "scopesifter outline PATH...", "List definitions in one or more source files in source order.")
		flags.PrintDefaults()
	}
	if showCommandHelp(flags, args) {
		return 0
	}
	paths, flagArgs, ok := splitPositionals(args)
	if !ok {
		flags.Usage()
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	view, err := navigator.New(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	absoluteRoot, err := filepath.Abs(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	options, err := common.buildOptions(navigator.IncludeDefs)
	if err != nil {
		printCLIError(err)
		return 2
	}
	if options.Limit > 0 && options.Limit < len(paths) {
		fmt.Fprintf(
			os.Stderr,
			"outline --limit %d is smaller than %d requested paths; use at least --limit %d\n",
			options.Limit,
			len(paths),
			len(paths),
		)
		return 2
	}
	remaining := options.Limit
	responses := make([]navigator.OutlineResponse, 0, len(paths))
	successCount := 0
	for index, path := range paths {
		pathOptions := options
		if options.Limit > 0 {
			remainingPaths := len(paths) - index
			pathOptions.Limit = fairResultLimit(remaining, remainingPaths)
		}
		response, err := view.Outline(path, pathOptions)
		if err != nil {
			if len(paths) == 1 {
				printCLIError(err)
				return 1
			}
			responses = append(responses, navigator.OutlineResponse{
				Path:    path,
				Root:    absoluteRoot,
				Results: []navigator.Result{},
				Error:   err.Error(),
			})
			continue
		}
		successCount++
		responses = append(responses, response)
		if remaining > 0 {
			remaining -= len(response.Results)
		}
	}
	if *common.jsonOut {
		if len(responses) == 1 {
			return printJSON(responses[0], *common.prettyJSON)
		}
		if status := printJSON(responses, *common.prettyJSON); status != 0 {
			return status
		}
		if successCount == 0 {
			return 1
		}
		return 0
	}
	for _, response := range responses {
		if response.Error != "" {
			fmt.Fprintf(
				os.Stderr,
				"%s: %s\n",
				safeOutputText(response.Path),
				safeOutputText(response.Error),
			)
			continue
		}
		printResults(response.Results, options.Return)
	}
	if successCount == 0 {
		return 1
	}
	return 0
}

func runChanged(args []string) int {
	flags := flag.NewFlagSet("changed", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnContext)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "scopesifter changed", "Return Git metadata, the exact patch, and changed-source context.")
		flags.PrintDefaults()
	}
	if showCommandHelp(flags, args) {
		return 0
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument:", safeOutputText(flags.Arg(0)))
		return 2
	}
	view, err := navigator.New(*common.root)
	if err != nil {
		printCLIError(err)
		return 1
	}
	opts, err := common.buildOptions(navigator.IncludeAll)
	if err != nil {
		printCLIError(err)
		return 2
	}
	response, err := view.Changed(opts)
	if err != nil {
		printCLIError(err)
		return 1
	}
	if *common.jsonOut {
		return printJSON(response, *common.prettyJSON)
	}
	printChangedResponse(response, opts.Return)
	return 0
}

type commonFlags struct {
	flags           *flag.FlagSet
	root            *string
	returnValue     *string
	context         *int
	limit           *int
	pathGlobs       *multiFlag
	excludeGlobs    *multiFlag
	changedOnly     *bool
	base            *string
	dropComments    *bool
	dropDocstrings  *bool
	noComments      *bool
	noStrings       *bool
	includeComments *bool
	includeStrings  *bool
	maxCodeLines    *int
	maxPatchLines   *int
	jsonOut         *bool
	prettyJSON      *bool
}

func addCommonFlags(flags *flag.FlagSet, defaultReturn navigator.Return) commonFlags {
	common := commonFlags{flags: flags}
	common.root = flags.String("root", ".", "repository root")
	common.returnValue = flags.String("return", returnName(defaultReturn), "locations, line, context, or scope")
	common.context = flags.Int("context", 5, "context lines for --return context")
	common.limit = flags.Int("limit", 50, "max results")
	common.pathGlobs = &multiFlag{}
	common.excludeGlobs = &multiFlag{}
	flags.Var(common.pathGlobs, "path", "include path glob/substring; repeatable")
	flags.Var(common.excludeGlobs, "exclude", "exclude path glob/substring; repeatable")
	common.changedOnly = flags.Bool("changed-only", false, "search only changed files (find/inspect)")
	common.base = flags.String("base", "", "Git base ref; changed compares REF...HEAD")
	common.dropComments = flags.Bool("drop-comments", false, "drop comments from embedded code")
	common.dropDocstrings = flags.Bool("drop-docstrings", false, "drop Python docstrings from embedded code")
	common.noComments = flags.Bool("no-comments", false, "ignore comment-only matches; default")
	common.noStrings = flags.Bool("no-strings", false, "ignore string-only matches; default")
	common.includeComments = flags.Bool("include-comments", false, "include comment-only matches; excluded by default")
	common.includeStrings = flags.Bool("include-strings", false, "include string-only matches; excluded by default")
	common.maxCodeLines = flags.Int("max-code-lines", 80, "max embedded code lines per result; omitted for --return locations; must be positive")
	common.maxPatchLines = flags.Int("max-patch-lines", 400, "max patch lines (changed only); must be positive")
	common.jsonOut = flags.Bool("json", false, "print JSON")
	common.prettyJSON = flags.Bool("pretty", false, "pretty-print JSON")
	return common
}

func (c commonFlags) buildOptions(include navigator.Include) (navigator.Options, error) {
	returnMode, err := c.resolvedReturn()
	if err != nil {
		return navigator.Options{}, err
	}
	if *c.maxCodeLines < 1 {
		return navigator.Options{}, fmt.Errorf("--max-code-lines must be positive; use --return locations to omit code")
	}
	if *c.maxPatchLines < 1 {
		return navigator.Options{}, fmt.Errorf("--max-patch-lines must be positive")
	}
	options := navigator.Options{
		Include:        include,
		Return:         returnMode,
		Context:        *c.context,
		ContextSet:     true,
		Limit:          *c.limit,
		PathGlobs:      append([]string(nil), (*c.pathGlobs)...),
		ExcludeGlobs:   append([]string(nil), (*c.excludeGlobs)...),
		ChangedOnly:    *c.changedOnly,
		Base:           *c.base,
		DropComments:   *c.dropComments,
		DropDocstrings: *c.dropDocstrings,
		NoComments:     *c.noComments || !*c.includeComments,
		NoStrings:      *c.noStrings || !*c.includeStrings,
		MaxCodeLines:   *c.maxCodeLines,
		MaxPatchLines:  *c.maxPatchLines,
	}
	return options, nil
}

func (c commonFlags) resolvedReturn() (navigator.Return, error) {
	switch *c.returnValue {
	case "locations":
		return navigator.ReturnLocations, nil
	case "line":
		return navigator.ReturnLine, nil
	case "context":
		return navigator.ReturnContext, nil
	case "scope":
		return navigator.ReturnScope, nil
	default:
		return "", fmt.Errorf("--return must be one of: locations, line, context, scope")
	}
}

func returnName(returnMode navigator.Return) string {
	if returnMode == navigator.ReturnLocations {
		return "locations"
	}
	return string(returnMode)
}

func validateInclude(command string, value navigator.Include, allowed ...navigator.Include) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	values := make([]string, len(allowed))
	for i, candidate := range allowed {
		values[i] = string(candidate)
	}
	return fmt.Errorf("%s --include must be one of: %s", command, strings.Join(values, ", "))
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, `scopesifter - Go code navigation with bounded source output

Usage:
  scopesifter find QUERY... [options]
  scopesifter inspect PATH:LINE... [options]
  scopesifter outline PATH... [options]
  scopesifter changed [options]

Commands:
  find      Exact identifiers or literal repository paths; reports matched evidence kind.
  inspect   Enclosing scope, optionally imports or related results; accepts multiple locations.
  outline   Definitions in one or more files, in source order.
  changed   Git metadata, exact patch, and changed-source context.

Return values:
  --return locations   Result metadata without source code.
  --return line        Matching or defining source line.
  --return context     Hit plus --context lines on each side.
  --return scope       Enclosing function, method, class, or block.

Use "scopesifter COMMAND --help" to list every option for that command.`)
}

func printCommandUsage(out io.Writer, syntax, description string) {
	fmt.Fprintf(out, "Usage: %s [options]\n\n%s\n\nOptions:\n", syntax, description)
}

func showCommandHelp(flags *flag.FlagSet, args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			flags.SetOutput(os.Stdout)
			flags.Usage()
			return true
		}
	}
	return false
}

func splitPositionals(args []string) ([]string, []string, bool) {
	flagArgs := make([]string, 0, len(args))
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if flagTakesValue(arg) && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return positionals, flagArgs, len(positionals) > 0
}

func flagTakesValue(flag string) bool {
	name := strings.TrimLeft(strings.SplitN(flag, "=", 2)[0], "-")
	switch name {
	case "root", "return", "context", "limit", "path",
		"exclude", "base", "include", "match", "max-code-lines", "max-patch-lines":
		return !strings.Contains(flag, "=")
	default:
		return false
	}
}

func printJSON(value any, pretty bool) int {
	encoder := json.NewEncoder(os.Stdout)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		printCLIError(err)
		return 1
	}
	return 0
}

func printChangedResponse(response navigator.ChangedResponse, returnMode navigator.Return) {
	if response.HeadCommit != "" {
		fmt.Printf("# head %s", safeOutputText(response.HeadCommit))
		if response.HeadSubject != "" {
			fmt.Printf(" %s", safeOutputText(response.HeadSubject))
		}
		fmt.Println()
	}
	if response.Base != "" {
		fmt.Printf("# base %s", safeOutputText(response.Base))
		if response.BaseCommit != "" {
			fmt.Printf(" %s", safeOutputText(response.BaseCommit))
		}
		fmt.Println()
	}
	if response.Patch != "" {
		fence := markdownCodeFence(response.Patch)
		fmt.Printf("%sdiff\n", fence)
		fmt.Println(response.Patch)
		fmt.Println(fence)
	}
	if response.PatchTruncated {
		fmt.Println("# patch truncated")
	}
	printResults(response.Results, returnMode)
}

func printResults(results []navigator.Result, returnMode navigator.Return) {
	for _, result := range results {
		if returnMode == navigator.ReturnLocations {
			path := safeOutputText(result.Path)
			switch {
			case result.Line > 0:
				fmt.Printf("%s:%d\n", path, result.Line)
			case result.StartLine > 0 && result.EndLine > 0:
				fmt.Printf("%s:%d\n", path, result.StartLine)
			default:
				fmt.Println(path)
			}
			continue
		}
		location := safeOutputText(result.Path)
		if result.StartLine > 0 && result.EndLine > 0 {
			location = fmt.Sprintf("%s:%d-%d", location, result.StartLine, result.EndLine)
		} else if result.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, result.Line)
		}
		fmt.Printf("# %s", location)
		if result.Kind != "" {
			fmt.Printf(" %s", result.Kind)
		}
		if result.Symbol != "" {
			fmt.Printf(" %s", safeOutputText(result.Symbol))
		}
		fmt.Println()
		if result.Finding == navigator.FindingFile && result.Code == "" {
			continue
		}
		fence := markdownCodeFence(result.Code)
		fmt.Printf("%s%s\n", fence, fenceLanguage(result.Path))
		fmt.Println(result.Code)
		fmt.Println(fence)
	}
}

func safeOutputText(value string) string {
	if !utf8.ValidString(value) || strings.IndexFunc(value, func(character rune) bool {
		return !unicode.IsPrint(character)
	}) >= 0 {
		return strconv.QuoteToGraphic(value)
	}
	return value
}

func printCLIError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, safeOutputText(err.Error()))
	}
}

func markdownCodeFence(source string) string {
	longest := 0
	current := 0
	for _, character := range source {
		if character == '`' {
			current++
			longest = max(longest, current)
			continue
		}
		current = 0
	}
	return strings.Repeat("`", max(3, longest+1))
}

func fenceLanguage(path string) string {
	switch filepath.Ext(path) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".c", ".h":
		return "c"
	case ".C", ".CC", ".CPP", ".CXX",
		".cc", ".cpp", ".cxx", ".c++", ".ii",
		".H", ".HH", ".HPP", ".HXX",
		".hpp", ".hh", ".hxx", ".h++",
		".ipp", ".tpp", ".tcc", ".inl",
		".ixx", ".cppm", ".mpp", ".ccm", ".cxxm", ".txx":
		return "cpp"
	case ".cs", ".csx":
		return "csharp"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".mod", ".def":
		return "modula-2"
	default:
		return ""
	}
}
