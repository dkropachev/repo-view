package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/dkropachev/repo-view/repoview"
)

// Match the JavaScript backend's 8 MiB source budget, with one Scanner-sized
// margin for a line terminator and read-ahead.
const maximumSourceLineBytes = (8 << 20) + bufio.MaxScanTokenSize

type fileData struct {
	path string
	rel  string
	ext  string
	line []string
}

type location struct {
	path string
	line int
}

type repositorySpec struct {
	name string
	url  string
}

var repositoryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("repo-view-validate", flag.ContinueOnError)
	repoList := flags.String("repo-list", "", "use existing repository paths from this file instead of managed clones")
	repoRoot := flags.String("repo-root", "", "scan an existing directory instead of using managed clones")
	repoSpec := flags.String("repo-spec", "testdata/validation-repos.tsv", "managed repository name/URL manifest")
	cloneRoot := flags.String("clone-root", "validation-repos", "directory for managed repository clones")
	casesPerRepo := flags.Int("cases", 100, "number of validation cases per repository")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *repoList != "" && *repoRoot != "" {
		fmt.Fprintln(os.Stderr, "--repo-list and --repo-root are mutually exclusive")
		return 2
	}

	var repos []string
	var source string
	var err error
	switch {
	case *repoList != "":
		repos, err = readRepoList(*repoList)
		source = *repoList
	case *repoRoot != "":
		repos, source, err = discoverRepos("", *repoRoot)
	default:
		repos, source, err = ensureManagedRepositories(*repoSpec, *cloneRoot)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "no repositories found")
		return 1
	}

	total := 0
	for _, repo := range repos {
		count, err := validateRepo(repo, *casesPerRepo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", repo, err)
			return 1
		}
		total += count
		fmt.Printf("ok %s cases=%d\n", repo, count)
	}
	fmt.Printf("validated repos=%d cases=%d source=%s\n", len(repos), total, source)
	return 0
}

func ensureManagedRepositories(specPath, cloneRoot string) ([]string, string, error) {
	specs, err := readRepositorySpecs(specPath)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(cloneRoot, 0o755); err != nil {
		return nil, "", fmt.Errorf("create clone root %s: %w", cloneRoot, err)
	}
	cloneRootInfo, err := os.Lstat(cloneRoot)
	if err != nil {
		return nil, "", fmt.Errorf("inspect clone root %s: %w", cloneRoot, err)
	}
	if !cloneRootInfo.IsDir() || cloneRootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("clone root %s is not a directory", cloneRoot)
	}

	repositories := make([]string, 0, len(specs))
	for _, spec := range specs {
		repository, cloned, err := ensureManagedRepository(cloneRoot, spec)
		if err != nil {
			return nil, "", err
		}
		if cloned {
			fmt.Fprintf(os.Stderr, "cloned %s into %s\n", spec.name, repository)
		} else {
			fmt.Fprintf(os.Stderr, "reusing %s at %s\n", spec.name, repository)
		}
		repositories = append(repositories, repository)
	}
	return repositories, specPath + " -> " + cloneRoot, nil
}

func readRepositorySpecs(path string) ([]repositorySpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open repository manifest %s: %w", path, err)
	}
	defer file.Close()

	seen := make(map[string]struct{})
	var specs []repositorySpec
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: expected repository name and URL", path, lineNumber)
		}
		name, url := fields[0], fields[1]
		if !repositoryNamePattern.MatchString(name) || name == "." || name == ".." {
			return nil, fmt.Errorf("%s:%d: invalid repository name %q", path, lineNumber, name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("%s:%d: duplicate repository name %q", path, lineNumber, name)
		}
		seen[name] = struct{}{}
		specs = append(specs, repositorySpec{name: name, url: url})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("repository manifest %s is empty", path)
	}
	return specs, nil
}

func ensureManagedRepository(cloneRoot string, spec repositorySpec) (string, bool, error) {
	target := filepath.Join(cloneRoot, spec.name)
	if _, err := os.Lstat(target); err == nil {
		return target, false, validateManagedRepository(target, spec.url)
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("inspect repository %s: %w", target, err)
	}

	stage, err := os.MkdirTemp(cloneRoot, ".clone-"+spec.name+"-")
	if err != nil {
		return "", false, fmt.Errorf("create clone stage for %s: %w", spec.name, err)
	}
	defer os.RemoveAll(stage)
	stagedRepository := filepath.Join(stage, "repository")
	command := exec.Command("git", "clone", "--depth", "1", "--no-tags", "--", spec.url, stagedRepository)
	if output, err := command.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("clone %s: %w\n%s", spec.url, err, output)
	}
	if err := os.Rename(stagedRepository, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return target, false, validateManagedRepository(target, spec.url)
		}
		return "", false, fmt.Errorf("publish clone %s: %w", target, err)
	}
	return target, true, nil
}

func validateManagedRepository(path, expectedOrigin string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed repository path %s is not a directory", path)
	}
	inside, err := gitOutput(path, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return fmt.Errorf("managed repository path %s is not a Git worktree", path)
	}
	origin, err := gitOutput(path, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read origin for %s: %w", path, err)
	}
	if origin != expectedOrigin {
		return fmt.Errorf("managed repository %s has origin %q, expected %q", path, origin, expectedOrigin)
	}
	return nil
}

func gitOutput(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func discoverRepos(repoList, repoRoot string) ([]string, string, error) {
	if info, err := os.Stat(repoList); err == nil && !info.IsDir() {
		repos, err := readRepoList(repoList)
		return repos, repoList, err
	}
	var repos []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			repos = append(repos, filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(repos)
	return repos, repoRoot + "/*.git", err
}

func readRepoList(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var repos []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		repos = append(repos, line)
	}
	sort.Strings(repos)
	return repos, scanner.Err()
}

func validateRepo(root string, cases int) (int, error) {
	files, err := readSourceFiles(root)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no source files")
	}
	index := buildIndex(files)
	symbols := selectSymbols(index, cases)
	if len(symbols) < cases {
		return 0, fmt.Errorf("only %d symbols available, need %d", len(symbols), cases)
	}
	view, err := repoview.New(root)
	if err != nil {
		return 0, err
	}

	maxCodeLines := 1
	for _, file := range files {
		if len(file.line) > maxCodeLines {
			maxCodeLines = len(file.line)
		}
	}
	for _, symbol := range symbols {
		want := locationsFor(index[symbol])
		locations, err := view.Find(symbol, repoview.Options{
			Include:      repoview.IncludeBoth,
			Return:       repoview.ReturnLocations,
			Limit:        len(want) + 1,
			MaxCodeLines: maxCodeLines,
		})
		if err != nil {
			return 0, fmt.Errorf("%s locations: %w", symbol, err)
		}
		if diff := compareLocationResults(locations.Results, want); diff != "" {
			return 0, fmt.Errorf("%s location mismatch: %s", symbol, diff)
		}

		scope, err := view.Find(symbol, repoview.Options{
			Include:      repoview.IncludeBoth,
			Return:       repoview.ReturnScope,
			Limit:        len(want) + 1,
			MaxCodeLines: maxCodeLines,
		})
		if err != nil {
			return 0, fmt.Errorf("%s scope: %w", symbol, err)
		}
		if err := validateScopes(scope.Results, files, symbol); err != nil {
			return 0, fmt.Errorf("%s scope invalid: %w", symbol, err)
		}
		if err := validateCode(scope.Results, files); err != nil {
			return 0, fmt.Errorf("%s code invalid: %w", symbol, err)
		}
	}
	return len(symbols), nil
}

func readSourceFiles(root string) ([]fileData, error) {
	extensions := map[string]bool{
		".c": true, ".cc": true, ".cjs": true, ".cpp": true, ".cs": true, ".def": true,
		".go": true, ".h": true, ".hpp": true, ".java": true, ".js": true, ".jsx": true,
		".kt": true, ".kts": true, ".mjs": true, ".py": true, ".rs": true, ".swift": true,
		".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".mod": true,
	}
	excludes := map[string]bool{
		".cache": true, ".git": true, ".hg": true, ".svn": true, ".venv": true,
		"build": true, "dist": true, "node_modules": true, "target": true,
		"vendor": true,
	}
	var files []fileData
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && excludes[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if !extensions[ext] {
			return nil
		}
		lines, err := readLines(path)
		if err != nil {
			return nil //nolint:nilerr // Unreadable source files are skipped during discovery.
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fileData{
			path: path,
			rel:  filepath.ToSlash(rel),
			ext:  ext,
			line: lines,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, err
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maximumSourceLineBytes)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func buildIndex(files []fileData) map[string][]location {
	index := map[string][]location{}
	ident := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	for _, file := range files {
		for i, line := range file.line {
			seen := map[string]bool{}
			for _, symbol := range ident.FindAllString(line, -1) {
				if seen[symbol] || isKeyword(symbol) || isModulaKeyword(file.ext, symbol) {
					continue
				}
				if !independentContainsSymbol(line, symbol) {
					continue
				}
				seen[symbol] = true
				index[symbol] = append(index[symbol], location{path: file.rel, line: i + 1})
			}
		}
	}
	return index
}

func selectSymbols(index map[string][]location, limit int) []string {
	type candidate struct {
		symbol string
		count  int
	}
	var candidates []candidate
	for symbol, locs := range index {
		if len(locs) < 2 || len(locs) > 200 {
			continue
		}
		candidates = append(candidates, candidate{symbol: symbol, count: len(locs)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count == candidates[j].count {
			return candidates[i].symbol < candidates[j].symbol
		}
		return candidates[i].count > candidates[j].count
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.symbol)
	}
	return out
}

func locationsFor(locs []location) []string {
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		out = append(out, fmt.Sprintf("%s:%d", loc.path, loc.line))
	}
	sort.Strings(out)
	return out
}

func compareLocationResults(got []repoview.Result, want []string) string {
	gotLocs := make([]string, 0, len(got))
	seen := make(map[string]bool, len(got))
	for _, result := range got {
		location := fmt.Sprintf("%s:%d", result.Path, result.Line)
		if seen[location] {
			continue
		}
		seen[location] = true
		gotLocs = append(gotLocs, location)
	}
	sort.Strings(gotLocs)
	if len(gotLocs) != len(want) {
		return fmt.Sprintf("got %d want %d first got=%q want=%q", len(gotLocs), len(want), first(gotLocs), first(want))
	}
	for i := range want {
		if gotLocs[i] != want[i] {
			return fmt.Sprintf("at %d got %q want %q", i, gotLocs[i], want[i])
		}
	}
	return ""
}

func validateScopes(results []repoview.Result, files []fileData, symbol string) error {
	fileMap := mapFiles(files)
	for _, result := range results {
		file, ok := fileMap[result.Path]
		if !ok {
			return fmt.Errorf("unknown file %s", result.Path)
		}
		if result.StartLine < 1 || result.EndLine < result.StartLine || result.EndLine > len(file.line) {
			return fmt.Errorf("bad range %s", resultLocation(result))
		}
		found := false
		for _, line := range file.line[result.StartLine-1 : result.EndLine] {
			if independentContainsSymbol(line, symbol) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("range lacks symbol %s", resultLocation(result))
		}
	}
	return nil
}

func validateCode(results []repoview.Result, files []fileData) error {
	fileMap := mapFiles(files)
	for _, result := range results {
		file, ok := fileMap[result.Path]
		if !ok {
			return fmt.Errorf("unknown file %s", result.Path)
		}
		codeStart, codeEnd := result.StartLine, result.EndLine
		if result.CodeStartLine > 0 {
			codeStart, codeEnd = result.CodeStartLine, result.CodeEndLine
		}
		if codeStart < 1 || codeEnd < codeStart || codeEnd > len(file.line) {
			return fmt.Errorf("bad code range %s", resultLocation(result))
		}
		want := strings.TrimRight(
			strings.Join(file.line[codeStart-1:codeEnd], "\n"),
			"\n",
		)
		if result.Code != want {
			return fmt.Errorf("%s code mismatch", resultLocation(result))
		}
	}
	return nil
}

func resultLocation(result repoview.Result) string {
	if result.StartLine > 0 && result.EndLine > 0 {
		return fmt.Sprintf("%s:%d-%d", result.Path, result.StartLine, result.EndLine)
	}
	return fmt.Sprintf("%s:%d", result.Path, result.Line)
}

func mapFiles(files []fileData) map[string]fileData {
	out := map[string]fileData{}
	for _, file := range files {
		out[file.rel] = file
	}
	return out
}

func independentContainsSymbol(line, symbol string) bool {
	for start := 0; ; {
		idx := strings.Index(line[start:], symbol)
		if idx < 0 {
			return false
		}
		pos := start + idx
		beforeOK := pos == 0 || !independentIdent(rune(line[pos-1]))
		after := pos + len(symbol)
		afterOK := after >= len(line) || !independentIdent(rune(line[after]))
		if beforeOK && afterOK {
			return true
		}
		start = pos + 1
	}
}

func independentIdent(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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

func isModulaKeyword(ext, symbol string) bool {
	if ext != ".mod" && ext != ".def" {
		return false
	}
	switch symbol {
	case "AND", "ARRAY", "ASM", "BEGIN", "BY", "CASE", "CONST", "DEFINITION", "DIV", "DO",
		"ELSE", "ELSIF", "END", "EXCEPT", "EXIT", "EXPORT", "FINALLY", "FOR", "FORWARD",
		"FROM", "IF", "IMPLEMENTATION", "IMPORT", "IN", "LOOP", "MOD", "MODULE", "NOT", "OF",
		"OR", "PACKEDSET", "POINTER", "PROCEDURE", "QUALIFIED", "RECORD", "REM", "REPEAT",
		"RETRY", "RETURN", "SET", "THEN", "TO", "TYPE", "UNQUALIFIED", "UNTIL", "VAR",
		"VOLATILE", "WHILE", "WITH", "__ATTRIBUTE__", "__BUILTIN__", "__COLUMN__",
		"__DATE__", "__FILE__", "__FUNCTION__", "__INLINE__", "__LINE__":
		return true
	default:
		return false
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
