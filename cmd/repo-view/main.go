package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dkropachev/repo-view/internal/runstats"
	"github.com/dkropachev/repo-view/repoview"
)

var (
	enforcedNavigationCommandCap     string
	enforcedNavigationTranscriptPath string
	enforcedLimitCap                 string
	enforcedContextCap               string
	enforcedMaxCodeLinesCap          string
	enforcedMaxPatchLinesCap         string
	enforcedNavigationRoot           string
	enforcedNavigationBaseCommit     string
	enforcedChangedReturn            string
	enforcedChangedContext           string
	enforcedNavigationSemantics      string
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
	default:
		printUsage(os.Stderr)
		return 2
	}
}

func runFind(args []string) int {
	flags := flag.NewFlagSet("find", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "repo-view find SYMBOL...", "Find definitions and references for one or more exact symbol names.")
		flags.PrintDefaults()
	}
	include := flags.String("include", "both", "defs, refs, or both")
	if showCommandHelp(flags, args) {
		return 0
	}
	symbols, flagArgs, ok := splitPositionals(args)
	if !ok {
		flags.Usage()
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument:", flags.Arg(0))
		return 2
	}
	if err := validateInclude("find", repoview.Include(*include), repoview.IncludeDefs, repoview.IncludeRefs, repoview.IncludeBoth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	view, err := repoview.New(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	options, err := common.buildOptions(repoview.Include(*include))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := common.enforceNavigationSemantics("find", options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if options.Limit > 0 && options.Limit < len(symbols) {
		fmt.Fprintf(
			os.Stderr,
			"find --limit %d is smaller than %d requested symbols; use at least --limit %d\n",
			options.Limit,
			len(symbols),
			len(symbols),
		)
		return 2
	}
	budget, err := consumeNavigationBudgetFor("find")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	responses, err := view.FindMany(symbols, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for index := range responses {
		responses[index].NavigationBudget = budget
	}
	if *common.jsonOut {
		return printJSON(responses, *common.prettyJSON)
	}
	for _, response := range responses {
		printResults(response.Results, options.Return)
	}
	return 0
}

func fairResultLimit(remaining, remainingSymbols int) int {
	return (remaining + remainingSymbols - 1) / remainingSymbols
}

func runInspect(args []string) int {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "repo-view inspect PATH:LINE...", "Return the enclosing scope and optionally imports or related results for one or more source locations.")
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
		repoview.Include(*include),
		repoview.IncludeSymbol,
		repoview.IncludeScope,
		repoview.IncludeDefs,
		repoview.IncludeRefs,
		repoview.IncludeBoth,
		repoview.IncludeImports,
		repoview.IncludeAll,
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	view, err := repoview.New(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	absoluteRoot, err := filepath.Abs(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	options, err := common.buildOptions(repoview.Include(*include))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := common.enforceNavigationSemantics("inspect", options); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	budget, err := consumeNavigationBudgetFor("inspect")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	remaining := options.Limit
	responses := make([]repoview.InspectResponse, 0, len(locations))
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
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			responses = append(responses, repoview.InspectResponse{
				Location:         location,
				Root:             absoluteRoot,
				Results:          []repoview.Result{},
				Error:            err.Error(),
				NavigationBudget: budget,
			})
			continue
		}
		successCount++
		response.NavigationBudget = budget
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
			fmt.Fprintf(os.Stderr, "%s: %s\n", response.Location, response.Error)
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
	common := addCommonFlags(flags, repoview.ReturnLine)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "repo-view outline PATH...", "List definitions in one or more source files in source order.")
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
	view, err := repoview.New(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	absoluteRoot, err := filepath.Abs(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	options, err := common.buildOptions(repoview.IncludeDefs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := common.enforceNavigationSemantics("outline", options); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	budget, err := consumeNavigationBudgetFor("outline")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	remaining := options.Limit
	responses := make([]repoview.OutlineResponse, 0, len(paths))
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
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			responses = append(responses, repoview.OutlineResponse{
				Path:             path,
				Root:             absoluteRoot,
				Results:          []repoview.Result{},
				Error:            err.Error(),
				NavigationBudget: budget,
			})
			continue
		}
		successCount++
		response.NavigationBudget = budget
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
			fmt.Fprintf(os.Stderr, "%s: %s\n", response.Path, response.Error)
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
	common := addCommonFlags(flags, repoview.ReturnContext)
	flags.Usage = func() {
		printCommandUsage(flags.Output(), "repo-view changed", "Return Git metadata, the exact patch, and changed-source context.")
		flags.PrintDefaults()
	}
	if showCommandHelp(flags, args) {
		return 0
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	view, err := repoview.New(*common.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	opts, err := common.buildOptions(repoview.IncludeAll)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := common.enforceNavigationSemantics("changed", opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	budget, err := consumeNavigationBudgetFor("changed")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	response, err := view.Changed(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if enabled, configErr := enforcedNavigationSemanticsEnabled(); configErr != nil {
		fmt.Fprintln(os.Stderr, configErr)
		return 2
	} else if enabled && response.BaseCommit != enforcedNavigationBaseCommit {
		fmt.Fprintf(
			os.Stderr,
			"changed response base commit %s does not match enforced base commit %s\n",
			response.BaseCommit,
			enforcedNavigationBaseCommit,
		)
		return 1
	}
	response.NavigationBudget = budget
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

func addCommonFlags(flags *flag.FlagSet, defaultReturn repoview.Return) commonFlags {
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

func (c commonFlags) buildOptions(include repoview.Include) (repoview.Options, error) {
	returnMode, err := c.resolvedReturn()
	if err != nil {
		return repoview.Options{}, err
	}
	maxCodeLinesSet := false
	c.flags.Visit(func(option *flag.Flag) {
		if option.Name == "max-code-lines" {
			maxCodeLinesSet = true
		}
	})
	if *c.maxCodeLines < 1 {
		return repoview.Options{}, fmt.Errorf("--max-code-lines must be positive; use --return locations to omit code")
	}
	if *c.maxPatchLines < 1 {
		return repoview.Options{}, fmt.Errorf("--max-patch-lines must be positive")
	}
	options := repoview.Options{
		Include:        include,
		Return:         returnMode,
		Context:        *c.context,
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
	for _, cap := range []struct {
		option string
		env    string
		value  int
	}{
		{option: "--limit", env: "REPO_VIEW_LIMIT_CAP", value: options.Limit},
		{option: "--context", env: "REPO_VIEW_CONTEXT_CAP", value: options.Context},
		{option: "--max-code-lines", env: "REPO_VIEW_MAX_CODE_LINES_CAP", value: options.MaxCodeLines},
		{option: "--max-patch-lines", env: "REPO_VIEW_MAX_PATCH_LINES_CAP", value: options.MaxPatchLines},
	} {
		if cap.option == "--max-code-lines" &&
			options.Return == repoview.ReturnLocations &&
			!maxCodeLinesSet {
			continue
		}
		if err := enforceOptionCap(cap.option, cap.value, cap.env); err != nil {
			return repoview.Options{}, err
		}
	}
	return options, nil
}

func (c commonFlags) enforceNavigationSemantics(
	subcommand string,
	options repoview.Options,
) error {
	enabled, err := enforcedNavigationSemanticsEnabled()
	if err != nil || !enabled {
		return err
	}

	visited := make(map[string]bool)
	c.flags.Visit(func(option *flag.Flag) {
		visited[option.Name] = true
	})
	for _, option := range []string{
		"root",
		"return",
		"context",
		"limit",
		"max-code-lines",
		"max-patch-lines",
		"json",
	} {
		if !visited[option] {
			return fmt.Errorf(
				"mechanically enforced navigation requires explicit --%s",
				option,
			)
		}
	}
	if !*c.jsonOut {
		return fmt.Errorf("mechanically enforced navigation requires --json")
	}
	if options.Limit < 1 {
		return fmt.Errorf("mechanically enforced navigation requires a positive --limit")
	}
	if options.MaxCodeLines < 1 {
		return fmt.Errorf(
			"mechanically enforced navigation requires a positive --max-code-lines",
		)
	}
	if options.MaxPatchLines < 1 {
		return fmt.Errorf(
			"mechanically enforced navigation requires a positive --max-patch-lines",
		)
	}
	if options.Context < 0 ||
		(options.Context == 0 && options.Return != repoview.ReturnLocations) {
		return fmt.Errorf(
			"mechanically enforced navigation requires a positive --context unless --return locations is used",
		)
	}

	root, err := canonicalDirectory(*c.root)
	if err != nil {
		return fmt.Errorf("resolve --root: %w", err)
	}
	requiredRoot, err := canonicalDirectory(enforcedNavigationRoot)
	if err != nil {
		return fmt.Errorf("resolve enforced navigation root: %w", err)
	}
	if root != requiredRoot {
		return fmt.Errorf(
			"--root resolves to %s, want enforced navigation root %s",
			root,
			requiredRoot,
		)
	}

	if subcommand != "changed" {
		return nil
	}
	if !visited["base"] {
		return fmt.Errorf(
			"mechanically enforced changed navigation requires explicit --base",
		)
	}
	if *c.base != enforcedNavigationBaseCommit {
		return fmt.Errorf(
			"--base %s does not match enforced base commit %s",
			*c.base,
			enforcedNavigationBaseCommit,
		)
	}
	if string(options.Return) != enforcedChangedReturn {
		return fmt.Errorf(
			"changed --return %s does not match enforced return %s",
			options.Return,
			enforcedChangedReturn,
		)
	}
	requiredContext, err := strconv.Atoi(enforcedChangedContext)
	if err != nil || requiredContext < 0 {
		return fmt.Errorf(
			"invalid mechanically enforced changed context: %q",
			enforcedChangedContext,
		)
	}
	if options.Context != requiredContext {
		return fmt.Errorf(
			"changed --context %d does not match enforced context %d",
			options.Context,
			requiredContext,
		)
	}
	for _, required := range []struct {
		option      string
		environment string
		value       int
	}{
		{option: "--limit", environment: "REPO_VIEW_LIMIT_CAP", value: options.Limit},
		{option: "--max-code-lines", environment: "REPO_VIEW_MAX_CODE_LINES_CAP", value: options.MaxCodeLines},
		{option: "--max-patch-lines", environment: "REPO_VIEW_MAX_PATCH_LINES_CAP", value: options.MaxPatchLines},
	} {
		raw, ok := enforcedOptionCap(required.environment)
		if !ok {
			return fmt.Errorf(
				"mechanically enforced changed navigation lacks %s",
				required.environment,
			)
		}
		expected, parseErr := strconv.Atoi(raw)
		if parseErr != nil || expected < 1 {
			return fmt.Errorf(
				"%s must be a positive integer: %s",
				required.environment,
				raw,
			)
		}
		if required.value != expected {
			return fmt.Errorf(
				"changed %s %d does not match enforced value %d",
				required.option,
				required.value,
				expected,
			)
		}
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	return filepath.Clean(resolved), nil
}

func enforcedNavigationSemanticsEnabled() (bool, error) {
	configured := enforcedNavigationSemantics != "" ||
		enforcedNavigationRoot != "" ||
		enforcedNavigationBaseCommit != "" ||
		enforcedChangedReturn != "" ||
		enforcedChangedContext != ""
	if !configured {
		return false, nil
	}
	if enforcedNavigationSemantics != "1" {
		return false, fmt.Errorf(
			"invalid mechanically enforced navigation semantics marker: %q",
			enforcedNavigationSemantics,
		)
	}
	if enforcedNavigationRoot == "" ||
		enforcedNavigationBaseCommit == "" ||
		enforcedChangedReturn == "" ||
		enforcedChangedContext == "" ||
		enforcedNavigationCommandCap == "" ||
		enforcedNavigationTranscriptPath == "" ||
		enforcedLimitCap == "" ||
		enforcedContextCap == "" ||
		enforcedMaxCodeLinesCap == "" ||
		enforcedMaxPatchLinesCap == "" {
		return false, fmt.Errorf(
			"mechanically enforced navigation semantics configuration is incomplete",
		)
	}
	if !validFullGitObjectID(enforcedNavigationBaseCommit) {
		return false, fmt.Errorf(
			"invalid mechanically enforced base commit: %q",
			enforcedNavigationBaseCommit,
		)
	}
	switch enforcedChangedReturn {
	case "locations", "line", "context", "scope":
	default:
		return false, fmt.Errorf(
			"invalid mechanically enforced changed return: %q",
			enforcedChangedReturn,
		)
	}
	return true, nil
}

func validFullGitObjectID(value string) bool {
	if (len(value) != 40 && len(value) != 64) ||
		strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func enforceOptionCap(option string, value int, environment string) error {
	raw, ok := enforcedOptionCap(environment)
	if !ok {
		return nil
	}
	limitCap, err := strconv.Atoi(raw)
	if err != nil || limitCap < 0 {
		return fmt.Errorf("%s must be a non-negative integer: %s", environment, raw)
	}
	if value == 0 {
		switch option {
		case "--limit":
			return fmt.Errorf("%s 0 disables the result limit while %s is set", option, environment)
		case "--context":
			value = 5
		case "--max-code-lines":
			return fmt.Errorf("%s must be positive; use --return locations to omit code", option)
		case "--max-patch-lines":
			return fmt.Errorf("%s must be positive", option)
		}
	}
	if value > limitCap {
		return fmt.Errorf("%s %d exceeds %s %d", option, value, environment, limitCap)
	}
	return nil
}

func enforcedOptionCap(environment string) (string, bool) {
	var enforced string
	switch environment {
	case "REPO_VIEW_LIMIT_CAP":
		enforced = enforcedLimitCap
	case "REPO_VIEW_CONTEXT_CAP":
		enforced = enforcedContextCap
	case "REPO_VIEW_MAX_CODE_LINES_CAP":
		enforced = enforcedMaxCodeLinesCap
	case "REPO_VIEW_MAX_PATCH_LINES_CAP":
		enforced = enforcedMaxPatchLinesCap
	}
	if enforced != "" {
		return enforced, true
	}
	return os.LookupEnv(environment)
}

func consumeNavigationBudget() (*repoview.NavigationBudget, error) {
	return consumeNavigationBudgetFor("")
}

func consumeNavigationBudgetFor(
	subcommand string,
) (*repoview.NavigationBudget, error) {
	rawLimit := enforcedNavigationCommandCap
	configured := rawLimit != ""
	if !configured {
		rawLimit, configured = os.LookupEnv("REPO_VIEW_NAVIGATION_COMMAND_CAP")
	}
	if !configured || rawLimit == "" || rawLimit == "0" {
		return nil, nil
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit < 1 {
		return nil, fmt.Errorf(
			"REPO_VIEW_NAVIGATION_COMMAND_CAP must be a positive integer: %s",
			rawLimit,
		)
	}
	if enforcedNavigationTranscriptPath != "" {
		return consumeNavigationTranscriptBudgetFor(
			enforcedNavigationTranscriptPath,
			limit,
			subcommand,
		)
	}
	if enabled, semanticsErr := enforcedNavigationSemanticsEnabled(); semanticsErr != nil {
		return nil, semanticsErr
	} else if enabled {
		return nil, fmt.Errorf(
			"mechanically enforced navigation requires a live transcript",
		)
	}
	statePath := os.Getenv("REPO_VIEW_NAVIGATION_BUDGET_FILE")
	if statePath == "" {
		return nil, fmt.Errorf(
			"REPO_VIEW_NAVIGATION_BUDGET_FILE is required when REPO_VIEW_NAVIGATION_COMMAND_CAP is set",
		)
	}
	return consumeNavigationFileBudget(statePath, limit)
}

func consumeNavigationFileBudget(
	statePath string,
	limit int,
) (_ *repoview.NavigationBudget, returnErr error) {
	release, err := acquireNavigationBudgetLock(statePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := release(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	if info, err := os.Lstat(statePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf(
				"navigation budget state must be a regular non-symlink file: %s",
				statePath,
			)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect navigation budget: %w", err)
	}
	state, err := os.OpenFile(statePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open navigation budget: %w", err)
	}
	defer state.Close()
	stateInfo, err := state.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect open navigation budget: %w", err)
	}
	pathInfo, err := os.Lstat(statePath)
	if err != nil {
		return nil, fmt.Errorf("inspect navigation budget after open: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		!os.SameFile(stateInfo, pathInfo) {
		return nil, fmt.Errorf(
			"navigation budget state changed while it was opened: %s",
			statePath,
		)
	}

	content, err := io.ReadAll(state)
	if err != nil {
		return nil, fmt.Errorf("read navigation budget: %w", err)
	}
	used := 0
	if value := strings.TrimSpace(string(content)); value != "" {
		used, err = strconv.Atoi(value)
		if err != nil || used < 0 {
			return nil, fmt.Errorf("invalid navigation budget state: %q", value)
		}
	}
	if used >= limit {
		return nil, fmt.Errorf("repo-view navigation command budget exhausted: %d/%d used", used, limit)
	}
	used++
	if err := state.Truncate(0); err != nil {
		return nil, fmt.Errorf("reset navigation budget: %w", err)
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek navigation budget: %w", err)
	}
	if _, err := fmt.Fprintln(state, used); err != nil {
		return nil, fmt.Errorf("write navigation budget: %w", err)
	}
	if err := state.Sync(); err != nil {
		return nil, fmt.Errorf("sync navigation budget: %w", err)
	}
	pathInfo, err = os.Lstat(statePath)
	if err != nil ||
		pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		!os.SameFile(stateInfo, pathInfo) {
		return nil, fmt.Errorf(
			"navigation budget state changed while it was updated: %s",
			statePath,
		)
	}
	return &repoview.NavigationBudget{
		Used:      used,
		Limit:     limit,
		Remaining: limit - used,
	}, nil
}

func acquireNavigationBudgetLock(statePath string) (func() error, error) {
	lockPath := statePath + ".lock"
	for attempt := 0; ; attempt++ {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create navigation budget lock: %w", err)
		}
		if attempt >= 999 {
			return nil, fmt.Errorf(
				"navigation budget lock is held or stale: %s",
				lockPath,
			)
		}
		time.Sleep(5 * time.Millisecond)
	}

	lock, err := os.Open(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open navigation budget lock: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("inspect navigation budget lock: %w", err)
	}
	pathInfo, err := os.Lstat(lockPath)
	if err != nil ||
		pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.IsDir() ||
		!os.SameFile(lockInfo, pathInfo) {
		_ = lock.Close()
		if err == nil && os.SameFile(lockInfo, pathInfo) {
			_ = os.Remove(lockPath)
		}
		return nil, fmt.Errorf(
			"navigation budget lock changed while it was acquired: %s",
			lockPath,
		)
	}

	released := false
	return func() error {
		if released {
			return nil
		}
		released = true
		defer lock.Close()
		current, err := os.Lstat(lockPath)
		if err != nil ||
			current.Mode()&os.ModeSymlink != 0 ||
			!current.IsDir() ||
			!os.SameFile(lockInfo, current) {
			return fmt.Errorf(
				"navigation budget lock changed while it was held: %s",
				lockPath,
			)
		}
		if err := os.Remove(lockPath); err != nil {
			return fmt.Errorf("remove navigation budget lock: %w", err)
		}
		return nil
	}, nil
}

func consumeNavigationTranscriptBudget(path string, limit int) (*repoview.NavigationBudget, error) {
	return consumeNavigationTranscriptBudgetFor(path, limit, "")
}

func consumeNavigationTranscriptBudgetFor(
	path string,
	limit int,
	subcommand string,
) (*repoview.NavigationBudget, error) {
	for range 100 {
		state, err := navigationTranscriptStateFor(path)
		if err != nil {
			return nil, err
		}
		if state.started > state.completed {
			if state.started > limit {
				return nil, fmt.Errorf("repo-view navigation command budget exhausted: %d/%d used", limit, limit)
			}
			if enabled, semanticsErr := enforcedNavigationSemanticsEnabled(); semanticsErr != nil {
				return nil, semanticsErr
			} else if enabled {
				if state.started != state.completed+1 {
					return nil, fmt.Errorf(
						"navigation transcript has %d unfinished repo-view commands",
						state.started-state.completed,
					)
				}
				if err := validateNavigationSequence(
					state.subcommands,
					subcommand,
				); err != nil {
					return nil, err
				}
			}
			return &repoview.NavigationBudget{
				Used:      state.started,
				Limit:     limit,
				Remaining: limit - state.started,
			}, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, fmt.Errorf("current repo-view invocation is missing from navigation transcript")
}

type navigationTranscriptState struct {
	subcommands []string
	started     int
	completed   int
}

func navigationTranscriptStateFor(
	path string,
) (navigationTranscriptState, error) {
	transcript, err := os.Open(path)
	if err != nil {
		return navigationTranscriptState{}, fmt.Errorf(
			"open navigation transcript: %w",
			err,
		)
	}
	defer transcript.Close()

	state := navigationTranscriptState{}
	scanner := bufio.NewScanner(transcript)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil ||
			event.Item.Type != "command_execution" {
			continue
		}
		subcommand, shapeErr := runstats.ValidatedRepoViewSubcommand(
			event.Item.Command,
		)
		if shapeErr != nil {
			return navigationTranscriptState{}, fmt.Errorf(
				"unsafe repo-view command in navigation transcript: %w",
				shapeErr,
			)
		}
		if subcommand == "" {
			continue
		}
		switch event.Type {
		case "item.started":
			state.started++
			state.subcommands = append(state.subcommands, subcommand)
		case "item.completed":
			state.completed++
		}
	}
	if err := scanner.Err(); err != nil {
		return navigationTranscriptState{}, fmt.Errorf(
			"read navigation transcript: %w",
			err,
		)
	}
	return state, nil
}

func validateNavigationSequence(
	subcommands []string,
	current string,
) error {
	if len(subcommands) == 0 {
		return fmt.Errorf("navigation transcript has no repo-view invocation")
	}
	if subcommands[0] != "changed" {
		return fmt.Errorf(
			"the first repo-view navigation invocation must be changed",
		)
	}
	changedCount := 0
	for _, subcommand := range subcommands {
		if subcommand == "changed" {
			changedCount++
		}
	}
	if changedCount != 1 {
		return fmt.Errorf(
			"repo-view changed must be invoked exactly once, got %d",
			changedCount,
		)
	}
	if current == "" || subcommands[len(subcommands)-1] != current {
		return fmt.Errorf(
			"current repo-view %s invocation is not the latest navigation transcript entry",
			current,
		)
	}
	return nil
}

func (c commonFlags) resolvedReturn() (repoview.Return, error) {
	switch *c.returnValue {
	case "locations":
		return repoview.ReturnLocations, nil
	case "line":
		return repoview.ReturnLine, nil
	case "context":
		return repoview.ReturnContext, nil
	case "scope":
		return repoview.ReturnScope, nil
	default:
		return "", fmt.Errorf("--return must be one of: locations, line, context, scope")
	}
}

func returnName(returnMode repoview.Return) string {
	if returnMode == repoview.ReturnLocations {
		return "locations"
	}
	return string(returnMode)
}

func validateInclude(command string, value repoview.Include, allowed ...repoview.Include) error {
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
	fmt.Fprintln(out, `repo-view - Go code navigation with bounded source output

Usage:
  repo-view find SYMBOL... [options]
  repo-view inspect PATH:LINE... [options]
  repo-view outline PATH... [options]
  repo-view changed [options]

Commands:
  find      Exact symbol definitions and references; accepts multiple symbols.
  inspect   Enclosing scope, optionally imports or related results; accepts multiple locations.
  outline   Definitions in one or more files, in source order.
  changed   Git metadata, exact patch, and changed-source context.

Return values:
  --return locations   Result metadata without source code.
  --return line        Matching or defining source line.
  --return context     Hit plus --context lines on each side.
  --return scope       Enclosing function, method, class, or block.

Use "repo-view COMMAND --help" to list every option for that command.`)
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
		"exclude", "base", "include", "max-code-lines", "max-patch-lines":
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
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func printChangedResponse(response repoview.ChangedResponse, returnMode repoview.Return) {
	if response.HeadCommit != "" {
		fmt.Printf("# head %s", response.HeadCommit)
		if response.HeadSubject != "" {
			fmt.Printf(" %s", response.HeadSubject)
		}
		fmt.Println()
	}
	if response.Base != "" {
		fmt.Printf("# base %s", response.Base)
		if response.BaseCommit != "" {
			fmt.Printf(" %s", response.BaseCommit)
		}
		fmt.Println()
	}
	if response.Patch != "" {
		fmt.Println("```diff")
		fmt.Println(response.Patch)
		fmt.Println("```")
	}
	if response.PatchTruncated {
		fmt.Println("# patch truncated")
	}
	printResults(response.Results, returnMode)
}

func printResults(results []repoview.Result, returnMode repoview.Return) {
	for _, result := range results {
		if returnMode == repoview.ReturnLocations {
			switch {
			case result.Line > 0:
				fmt.Printf("%s:%d\n", result.Path, result.Line)
			case result.StartLine > 0 && result.EndLine > 0:
				fmt.Printf("%s:%d\n", result.Path, result.StartLine)
			default:
				fmt.Println(result.Path)
			}
			continue
		}
		location := result.Path
		if result.StartLine > 0 && result.EndLine > 0 {
			location = fmt.Sprintf("%s:%d-%d", result.Path, result.StartLine, result.EndLine)
		} else if result.Line > 0 {
			location = fmt.Sprintf("%s:%d", result.Path, result.Line)
		}
		fmt.Printf("# %s", location)
		if result.Kind != "" {
			fmt.Printf(" %s", result.Kind)
		}
		if result.Symbol != "" {
			fmt.Printf(" %s", result.Symbol)
		}
		fmt.Println()
		fmt.Printf("```%s\n", fenceLanguage(result.Path))
		fmt.Println(result.Code)
		fmt.Println("```")
	}
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
	case ".cc", ".cpp", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	default:
		return ""
	}
}
