package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/navigator"
)

func TestSplitPositionalsAllowsFlagsAfterSymbol(t *testing.T) {
	symbols, flags, ok := splitPositionals([]string{"helper", "--root", ".", "--return", "scope", "--include", "refs"})
	if !ok {
		t.Fatal("symbol not found")
	}
	if len(symbols) != 1 || symbols[0] != "helper" {
		t.Fatalf("symbols = %#v", symbols)
	}
	want := []string{"--root", ".", "--return", "scope", "--include", "refs"}
	if len(flags) != len(want) {
		t.Fatalf("flags = %#v", flags)
	}
	for i := range want {
		if flags[i] != want[i] {
			t.Fatalf("flags[%d] = %q, want %q", i, flags[i], want[i])
		}
	}
}

func TestReturnLocationsMapsToNoEmbeddedCode(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	if err := flags.Parse([]string{"--return", "locations"}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(navigator.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if options.Return != navigator.ReturnLocations {
		t.Fatalf("return = %q", options.Return)
	}
}

func TestCommonFlagsPreserveExplicitZeroContext(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	if err := flags.Parse([]string{"--return", "locations", "--context", "0"}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(navigator.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if options.Context != 0 || !options.ContextSet {
		t.Fatalf("context = %d, context set = %t", options.Context, options.ContextSet)
	}
}

func TestFenceLanguageCoversJavaScriptAndTypeScriptExtensions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "script.js", want: "javascript"},
		{path: "module.mjs", want: "javascript"},
		{path: "common.cjs", want: "javascript"},
		{path: "view.jsx", want: "jsx"},
		{path: "source.ts", want: "typescript"},
		{path: "view.tsx", want: "tsx"},
		{path: "module.mts", want: "typescript"},
		{path: "common.cts", want: "typescript"},
	} {
		if got := fenceLanguage(test.path); got != test.want {
			t.Fatalf("fenceLanguage(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestFenceLanguageCoversKotlinSourceAndScriptExtensions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"source.kt", "build.gradle.kts"} {
		if got := fenceLanguage(path); got != "kotlin" {
			t.Fatalf("fenceLanguage(%q) = %q, want kotlin", path, got)
		}
	}
}

func TestFenceLanguageCoversSwiftSourceExtension(t *testing.T) {
	t.Parallel()

	if got := fenceLanguage("Sources/App/Service.swift"); got != "swift" {
		t.Fatalf("fenceLanguage(Service.swift) = %q, want swift", got)
	}
}

func TestFenceLanguageCoversModulaSourceExtensions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"src/Program.mod", "lib/Storage.def"} {
		if got := fenceLanguage(path); got != "modula-2" {
			t.Fatalf("fenceLanguage(%q) = %q, want modula-2", path, got)
		}
	}
}

func TestFenceLanguageCoversCSharpAndRegisteredCPPExtensions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"Source.cs", "Script.csx"} {
		if got := fenceLanguage(path); got != "csharp" {
			t.Fatalf("fenceLanguage(%q) = %q, want csharp", path, got)
		}
	}
	for _, path := range []string{
		"source.CXX", "source.c++", "header.HXX", "template.tpp", "module.cppm",
	} {
		if got := fenceLanguage(path); got != "cpp" {
			t.Fatalf("fenceLanguage(%q) = %q, want cpp", path, got)
		}
	}
}

func TestPrintResultsUsesFenceLongerThanEmbeddedBackticks(t *testing.T) {
	output := captureStdout(t, func() {
		printResults([]navigator.Result{{
			Path: "found.go", StartLine: 1, EndLine: 3,
			Code: "before\n```\nafter",
		}}, navigator.ReturnScope)
	})
	if !strings.Contains(output, "````go\nbefore\n```\nafter\n````\n") {
		t.Fatalf("fenced output = %q", output)
	}
}

func TestPrintLocationsUsesPointLine(t *testing.T) {
	output := captureStdout(t, func() {
		printResults([]navigator.Result{{
			Path:      "found.go",
			Line:      7,
			StartLine: 4,
			EndLine:   10,
		}}, navigator.ReturnLocations)
	})
	if output != "found.go:7\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestPrintLocationsUsesRangeStartWithoutPointLine(t *testing.T) {
	output := captureStdout(t, func() {
		printResults([]navigator.Result{{
			Path:      "found.go",
			StartLine: 4,
			EndLine:   10,
		}}, navigator.ReturnLocations)
	})
	if output != "found.go:4\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestPrintResultsEscapesControlCharactersInRepositoryPaths(t *testing.T) {
	const hostilePath = "safe.go\n\x1b[31m# injected"
	const escapedPath = `"safe.go\n\x1b[31m# injected"`

	locations := captureStdout(t, func() {
		printResults([]navigator.Result{{
			Path: hostilePath,
			Line: 7,
		}}, navigator.ReturnLocations)
	})
	if locations != escapedPath+":7\n" {
		t.Fatalf("location output = %q", locations)
	}

	scopes := captureStdout(t, func() {
		printResults([]navigator.Result{{
			Path:      hostilePath,
			StartLine: 4,
			EndLine:   6,
			Code:      "safe code",
		}}, navigator.ReturnScope)
	})
	if !strings.HasPrefix(scopes, "# "+escapedPath+":4-6\n") ||
		strings.Contains(scopes, "\n\x1b[31m# injected") {
		t.Fatalf("scope output = %q", scopes)
	}
}

func TestPrintChangedResponseEscapesControlCharactersInMetadata(t *testing.T) {
	output := captureStdout(t, func() {
		printChangedResponse(navigator.ChangedResponse{
			HeadCommit:  "abc123",
			HeadSubject: "safe\n# injected\x1b[31m",
			Base:        "main\x1b[2J",
			BaseCommit:  "def456",
		}, navigator.ReturnLocations)
	})
	if strings.Contains(output, "\n# injected") || strings.ContainsRune(output, '\x1b') ||
		!strings.Contains(output, `"safe\n# injected\x1b[31m"`) ||
		!strings.Contains(output, `"main\x1b[2J"`) {
		t.Fatalf("changed metadata output = %q", output)
	}
}

func TestSingleInspectErrorEscapesControlCharactersInPath(t *testing.T) {
	root := t.TempDir()
	const name = "hostile\n\x1b[31m.go"
	if err := os.WriteFile(
		filepath.Join(root, name),
		[]byte("package demo\n"),
		0o600,
	); err != nil {
		t.Skipf("control-character filenames unavailable: %v", err)
	}

	status := 0
	output := captureStderr(t, func() {
		status = run([]string{
			"inspect", name + ":99", "--root", root, "--return", "line",
		})
	})
	if status != 1 || strings.ContainsRune(output, '\x1b') ||
		strings.Contains(output, "hostile\n") ||
		!strings.Contains(output, `hostile\n\x1b[31m.go`) {
		t.Fatalf("status = %d, stderr = %q", status, output)
	}
}

func TestReturnRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"invalid"} {
		t.Run(value, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			common := addCommonFlags(flags, navigator.ReturnScope)
			if err := flags.Parse([]string{"--return", value}); err != nil {
				t.Fatal(err)
			}
			_, err := common.buildOptions(navigator.IncludeBoth)
			if err == nil || !strings.Contains(err.Error(), "--return must be one of") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestChangedRejectsPositionalArguments(t *testing.T) {
	if status := run([]string{
		"changed",
		"unexpected-positional",
		"--root", "/definitely/not/a/repository",
		"--json",
	}); status != 2 {
		t.Fatalf("changed status = %d, want 2", status)
	}
}

func TestLegacyLauncherEnvironmentCannotAffectOrdinaryCLI(t *testing.T) {
	for name, value := range map[string]string{
		"SCOPESIFTER_LIMIT_CAP":              "0",
		"SCOPESIFTER_CONTEXT_CAP":            "invalid",
		"SCOPESIFTER_MAX_CODE_LINES_CAP":     "0",
		"SCOPESIFTER_MAX_PATCH_LINES_CAP":    "0",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP": "1",
		"SCOPESIFTER_NAVIGATION_BUDGET_FILE": filepath.Join(t.TempDir(), "budget"),
		"SCOPESIFTER_REASONING_EFFORT":       "ultra",
		"SCOPESIFTER_ANSWER_GUARD":           "on",
	} {
		t.Setenv(name, value)
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	if err := flags.Parse([]string{
		"--limit", "51",
		"--context", "6",
		"--max-code-lines", "81",
		"--max-patch-lines", "401",
	}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(navigator.IncludeBoth)
	if err != nil {
		t.Fatalf("legacy launcher environment affected options: %v", err)
	}
	if options.Limit != 51 || options.Context != 6 ||
		options.MaxCodeLines != 81 || options.MaxPatchLines != 401 {
		t.Fatalf("options = %#v", options)
	}

	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "demo.go"),
		[]byte("package demo\n\nfunc Target() {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var status int
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			status = run([]string{
				"find", "Target", "--root", root, "--include", "defs",
				"--return", "locations", "--json",
			})
		})
	})
	if status != 0 {
		t.Fatalf("status = %d, stderr = %s", status, stderr)
	}
	if strings.Contains(stdout, "navigation_budget") {
		t.Fatalf("ordinary JSON retained launcher budget: %s", stdout)
	}
}

func TestLauncherIntegrationIsStructurallyAbsent(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{
		"cmd/scopesifter-codex",
		"internal/codexlauncher",
		"internal/navigationcommand",
		"make/launcher.mk",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("removed launcher path %q still exists: %v", path, err)
		}
	}
	for _, path := range []string{"Makefile", "README.md"} {
		contents, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"scopesifter-codex",
			"make/launcher.mk",
			"SCOPESIFTER_ANSWER_GUARD",
			"SCOPESIFTER_NAVIGATION_COMMAND_CAP",
		} {
			if strings.Contains(string(contents), forbidden) {
				t.Fatalf("%s retains launcher documentation or target %q", path, forbidden)
			}
		}
	}

	command := exec.Command("go", "list", "-deps", "./cmd/scopesifter")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list scopesifter dependencies: %v\n%s", err, output)
	}
	for _, forbidden := range []string{
		"github.com/yapless/scopesifter/internal/codexlauncher",
		"github.com/yapless/scopesifter/internal/navigationcommand",
	} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("scopesifter command graph retains %q:\n%s", forbidden, output)
		}
	}
}

func TestMCPIntegrationIsStructurallyAbsent(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{
		"cmd/scopesifter/mcp.go",
		"cmd/scopesifter/mcp_test.go",
		"internal/scopesiftermcp",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("removed MCP path %q still exists: %v", path, err)
		}
	}

	status := 0
	stderr := captureStderr(t, func() {
		status = run([]string{"mcp"})
	})
	if status != 2 {
		t.Fatalf("removed mcp command status = %d, want 2; stderr = %q", status, stderr)
	}

	help := captureStdout(t, func() {
		status = run([]string{"--help"})
	})
	if status != 0 {
		t.Fatalf("help status = %d, want 0", status)
	}
	if strings.Contains(strings.ToLower(help), "mcp") {
		t.Fatalf("help retains MCP command:\n%s", help)
	}

	for _, path := range []string{"README.md", "cmd/scopesifter/main.go"} {
		contents, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(contents)), "mcp") {
			t.Fatalf("%s retains MCP product text", path)
		}
	}

	manifest, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"github.com/modelcontextprotocol/go-sdk",
		"github.com/yapless/scopesifter/internal/scopesiftermcp",
	} {
		if strings.Contains(string(manifest), forbidden) {
			t.Fatalf("go.mod retains removed dependency %q", forbidden)
		}
	}

	command := exec.Command("go", "list", "-deps", "-test", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list repository dependencies: %v\n%s", err, output)
	}
	for _, forbidden := range []string{
		"github.com/modelcontextprotocol/go-sdk",
		"github.com/yapless/scopesifter/internal/scopesiftermcp",
	} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("repository dependency graph retains %q:\n%s", forbidden, output)
		}
	}
}

func TestNavigationRejectsZeroCodeAndPatchLimits(t *testing.T) {
	tests := []struct {
		option string
		want   string
	}{
		{"--max-code-lines", "use --return locations to omit code"},
		{"--max-patch-lines", "--max-patch-lines must be positive"},
	}
	for _, test := range tests {
		t.Run(test.option, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			common := addCommonFlags(flags, navigator.ReturnScope)
			if err := flags.Parse([]string{test.option, "0"}); err != nil {
				t.Fatal(err)
			}
			_, err := common.buildOptions(navigator.IncludeBoth)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLocationReturnAcceptsExplicitPositiveCodeLineLimit(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	if err := flags.Parse([]string{
		"--return", "locations",
		"--max-code-lines", "61",
	}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(navigator.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if options.Return != navigator.ReturnLocations {
		t.Fatalf("return = %q", options.Return)
	}
	if options.MaxCodeLines != 61 {
		t.Fatalf("max code lines = %d", options.MaxCodeLines)
	}
}

func TestReleaseArchivesCarryCompleteThirdPartyNotices(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	manifest := read("go.mod")
	notices := read("THIRD_PARTY_NOTICES.md")
	workflow := read(filepath.Join(".github", "workflows", "release.yml"))
	releaseMakefile := read(filepath.Join("make", "release.mk"))

	for _, module := range []string{
		"github.com/dcosson/treesitter-go",
		"golang.org/x/text",
	} {
		fields := strings.Fields(manifest)
		version := ""
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == module {
				version = fields[index+1]
				break
			}
		}
		if version == "" {
			t.Fatalf("direct module %s is absent from go.mod", module)
		}
		if !strings.Contains(notices, module) ||
			!strings.Contains(notices, "version `"+version+"`") {
			t.Errorf("notices do not identify %s %s", module, version)
		}
	}
	for _, marker := range []string{
		"Copyright (c) 2014-2023 Max Brunsfeld, Damien Guard, Amaan Qureshi",
		"Copyright (c) 2019 fwcd",
		"Copyright (c) 2021 alex-pinkus",
		"Copyright (c) 2026 Danny Cosson",
		"tree-sitter `v0.26.6`",
		"`tree-sitter-c`",
		"`v0.24.1`",
		"`tree-sitter-cpp`",
		"`v0.23.4`",
		"`tree-sitter-java`",
		"`v0.23.5`",
		"`tree-sitter-javascript`",
		"`v0.25.0`",
		"`tree-sitter-typescript`",
		"`v0.23.2`, including its TypeScript and TSX grammars",
		"`tree-sitter-python`",
		"`v0.23.6`",
		"`tree-sitter-rust`",
		"`v0.24.0`",
		"Copyright (c) 2018 Max Brunsfeld (tree-sitter runtime)",
		"Copyright (c) 2017 Ayman Nadeem (Java grammar)",
		"Copyright (c) 2016 Max Brunsfeld (Python grammar)",
		"Copyright (c) 2017 Maxim Sokolov (Rust grammar)",
		"Copyright 2009 The Go Authors.",
		"Redistribution and use in source and binary forms",
		"Additional IP Rights Grant (Patents)",
		"Copyright © 1991-2026 Unicode, Inc.",
		"unicode-ident 1.0.24",
		"navigator/javascript_unicode.go",
		"navigator/python_xid.go",
		"Permission is hereby granted, free of charge",
	} {
		if !strings.Contains(notices, marker) {
			t.Errorf("third-party notices lack %q", marker)
		}
	}
	if !strings.Contains(workflow, "shell: go run -mod=readonly ./internal/cmd/workflow-runner -- {0}") ||
		strings.Count(workflow, "runs-on: ubuntu-24.04") != 2 ||
		!strings.Contains(workflow, "go-version: \"1.26.5\"") ||
		strings.Count(workflow, "uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e") != 1 ||
		!strings.Contains(workflow, "needs: test") ||
		!strings.Contains(workflow, "environment: release") ||
		!strings.Contains(workflow, "container: golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f") ||
		!strings.Contains(workflow, "run: release-artifacts") ||
		strings.Count(workflow, "uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6") != 2 ||
		!strings.Contains(workflow, "subject-checksums: dist/SHA256SUMS") ||
		!strings.Contains(workflow, "subject-path: dist/SHA256SUMS") ||
		!strings.Contains(workflow, "GH_TOKEN: ${{ secrets.SCOPESIFTER_RELEASE_TOKEN }}") ||
		!strings.Contains(workflow, "run: release-publish") ||
		!strings.Contains(releaseMakefile, "go run -mod=readonly ./internal/cmd/release-artifacts -mode build") ||
		!strings.Contains(releaseMakefile, "go run -mod=readonly ./internal/cmd/release-artifacts -mode publish") {
		t.Fatal("release workflow does not use the Go target runner and Go release tool")
	}
	if strings.Contains(workflow, "shell: bash") || strings.Contains(workflow, "shell: sh") {
		t.Fatal("release workflow must not use a script-runtime shell")
	}
}

func TestCommonFlagsPreserveRepeatablePathFilters(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, navigator.ReturnScope)
	if err := flags.Parse([]string{
		"--path", "service/matching",
		"--path", "common/quotas/**",
		"--exclude", "*_test.go",
	}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(navigator.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(options.PathGlobs, ","); got != "service/matching,common/quotas/**" {
		t.Fatalf("path globs = %q", got)
	}
	if got := strings.Join(options.ExcludeGlobs, ","); got != "*_test.go" {
		t.Fatalf("exclude globs = %q", got)
	}
}

func TestSplitPositionalsAcceptsSingleDashValueFlag(t *testing.T) {
	symbols, flags, ok := splitPositionals([]string{"helper", "-root", ".", "-return", "scope"})
	if !ok || len(symbols) != 1 || symbols[0] != "helper" {
		t.Fatalf("symbols = %#v", symbols)
	}
	if got := strings.Join(flags, " "); got != "-root . -return scope" {
		t.Fatalf("flags = %q", got)
	}
}

func TestValidateFindIncludeRejectsInspectOnlyValue(t *testing.T) {
	err := validateInclude("find", navigator.IncludeAll, navigator.IncludeDefs, navigator.IncludeRefs, navigator.IncludeBoth)
	if err == nil || !strings.Contains(err.Error(), "find --include") {
		t.Fatalf("error = %v", err)
	}
}

func TestFairResultLimitReservesBudgetForEverySymbol(t *testing.T) {
	remaining := 20
	var got []int
	for symbols := 6; symbols > 0; symbols-- {
		share := fairResultLimit(remaining, symbols)
		got = append(got, share)
		remaining -= share
	}
	want := []int{4, 4, 3, 3, 3, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shares = %#v, want %#v", got, want)
		}
	}
}

func TestInspectAcceptsMultipleLocations(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc first() {}\n\nfunc second() {}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"inspect",
			"demo.go:3",
			"demo.go:5",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "2",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []navigator.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %#v", responses)
	}
	if responses[0].Location != "demo.go:3" || responses[1].Location != "demo.go:5" {
		t.Fatalf("locations = %q, %q", responses[0].Location, responses[1].Location)
	}
	if len(responses[0].Results) != 1 || len(responses[1].Results) != 1 {
		t.Fatalf("results = %#v", responses)
	}
}

func TestInspectDefaultsToScopeOnly(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"inspect",
			"demo.go:6",
			"--root", root,
			"--return", "scope",
			"--limit", "20",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var response navigator.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "scope" ||
		response.Results[0].Scope != "caller" {
		t.Fatalf("response = %#v", response)
	}
}

func TestInspectBatchRetainsValidLocationsWhenOneIsInvalid(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc first() {}\n\nfunc second() {}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"inspect",
			"demo.go:3",
			"demo.go:99",
			"demo.go:5",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "3",
			"--json",
		})
	})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}

	var responses []navigator.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[2].Results) != 1 {
		t.Fatalf("valid results discarded: %#v", responses)
	}
	if responses[1].Location != "demo.go:99" ||
		!strings.Contains(responses[1].Error, "line 99 out of range") ||
		responses[1].Results == nil {
		t.Fatalf("invalid response = %#v", responses[1])
	}
}

func TestInspectBatchFailsWhenEveryLocationIsInvalid(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "demo.go"),
		[]byte("package demo\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"inspect",
			"demo.go:2",
			"demo.go:3",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "2",
			"--json",
		})
	})
	if status == 0 {
		t.Fatal("expected all-invalid batch to fail")
	}

	var responses []navigator.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Error == "" || responses[1].Error == "" {
		t.Fatalf("responses = %#v", responses)
	}
}

func TestFindHonorsPathFilterAndUsesEmptyArrays(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first/one.go":          "package first\n\nfunc target() {}\nfunc caller() { target() }\n",
		"first/skipped_test.go": "package first\n\nfunc callerTest() { target() }\n",
		"second/two.go":         "package second\n\nfunc target() {}\nfunc caller() { target() }\n",
	} {
		absolute := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"find",
			"target",
			"missing",
			"--root", root,
			"--include", "refs",
			"--return", "locations",
			"--path", "first",
			"--exclude", "*_test.go",
			"--limit", "4",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []navigator.FindResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || len(responses[0].Results) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	if got := responses[0].Results[0].Path; got != "first/one.go" {
		t.Fatalf("path = %q", got)
	}
	if responses[1].Results == nil || len(responses[1].Results) != 0 {
		t.Fatalf("missing results = %#v", responses[1].Results)
	}
	if !strings.Contains(stdout, `"results":[]`) {
		t.Fatalf("empty results are not an array: %s", stdout)
	}
}

func TestFindCLIReportsPathFindingWithoutEmptyCodeFence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cmd", "scopesifter-validate", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	jsonOutput := captureStdout(t, func() {
		if status := run([]string{
			"find", "scopesifter-validate", "--root", root, "--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})
	var responses []navigator.FindResponse
	if err := json.Unmarshal([]byte(jsonOutput), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, jsonOutput)
	}
	if len(responses) != 1 || responses[0].Query != "scopesifter-validate" ||
		responses[0].MatchedAs != navigator.FindOutcomeFile ||
		len(responses[0].Results) != 1 ||
		responses[0].Results[0].Finding != navigator.FindingFile ||
		responses[0].Results[0].Kind != "file" ||
		responses[0].Results[0].Path != "cmd/scopesifter-validate/settings.yaml" {
		t.Fatalf("responses = %#v", responses)
	}
	if strings.Contains(jsonOutput, `"symbol":"scopesifter-validate"`) {
		t.Fatalf("path query mislabeled as symbol: %s", jsonOutput)
	}

	plainOutput := captureStdout(t, func() {
		if status := run([]string{
			"find", "scopesifter-validate", "--root", root,
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})
	if plainOutput != "# scopesifter-validate: file\n# cmd/scopesifter-validate/settings.yaml file\n" ||
		strings.Contains(plainOutput, "```") {
		t.Fatalf("plain output = %q", plainOutput)
	}
}

func TestFindCLIPlainOutputReportsNoMatchAndHint(t *testing.T) {
	root := t.TempDir()
	output := captureStdout(t, func() {
		if status := run([]string{"find", "missing", "--root", root}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})
	if !strings.HasPrefix(output, "# missing: none\n# No exact source identifier") {
		t.Fatalf("plain output = %q", output)
	}
}

func TestFindCLIPlainLocationsReportClassification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.go"), []byte("package demo\n\nfunc Target() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if status := run([]string{
			"find", "Target", "--root", root, "--return", "locations",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})
	if output != "# Target: symbol\ntarget.go:3\n" {
		t.Fatalf("plain output = %q", output)
	}
}

func TestFindCLIRejectsUnknownMatchMode(t *testing.T) {
	status := 0
	output := captureStderr(t, func() {
		status = run([]string{"find", "Target", "--match", "text"})
	})
	if status != 2 || !strings.Contains(output, "auto, symbol, path") {
		t.Fatalf("status = %d, want 2", status)
	}
}

func TestFindFairLimitReturnsEveryRequestedSymbol(t *testing.T) {
	root := t.TempDir()
	source := `package demo

func Alpha() {}
func Bravo() {}
func Charlie() {}
func Delta() {}
func Echo() {}
func Foxtrot() {}
`
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"find",
			"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot",
			"--root", root,
			"--include", "defs",
			"--return", "locations",
			"--limit", "6",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []navigator.FindResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 6 {
		t.Fatalf("responses = %#v", responses)
	}
	for index, response := range responses {
		if len(response.Results) != 1 {
			t.Fatalf("responses[%d] = %#v", index, response)
		}
	}
}

func TestOutlineAcceptsMultiplePaths(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first.go":  "package demo\n\nfunc first() {}\n",
		"second.go": "package demo\n\nfunc second() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"outline",
			"first.go",
			"second.go",
			"--root", root,
			"--return", "line",
			"--limit", "2",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []navigator.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Path != "first.go" || responses[1].Path != "second.go" {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[1].Results) != 1 {
		t.Fatalf("results = %#v", responses)
	}
}

func TestSwiftCLIOutlineFindAndInspectUseDedicatedBackend(t *testing.T) {
	root := t.TempDir()
	const source = `import Foundation

final class Service {
    let value = 1
    func run() {
        Target()
    }
}
`
	if err := os.WriteFile(filepath.Join(root, "Service.swift"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	outlineJSON := captureStdout(t, func() {
		if status := run([]string{
			"outline", "Service.swift",
			"--root", root,
			"--return", "locations",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("outline status = %d", status)
		}
	})
	var outline navigator.OutlineResponse
	if err := json.Unmarshal([]byte(outlineJSON), &outline); err != nil {
		t.Fatalf("decode Swift outline: %v\n%s", err, outlineJSON)
	}
	wantSymbols := []string{"Service", "value", "run"}
	if len(outline.Results) != len(wantSymbols) {
		t.Fatalf("Swift outline = %#v, want %v", outline.Results, wantSymbols)
	}
	for index, want := range wantSymbols {
		result := outline.Results[index]
		if result.Symbol != want || result.Kind != "def" || result.Language != "swift" {
			t.Errorf("Swift outline result %d = %#v, want %q definition", index, result, want)
		}
	}

	findJSON := captureStdout(t, func() {
		if status := run([]string{
			"find", "Target",
			"--root", root,
			"--include", "refs",
			"--return", "scope",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("find status = %d", status)
		}
	})
	var found []navigator.FindResponse
	if err := json.Unmarshal([]byte(findJSON), &found); err != nil {
		t.Fatalf("decode Swift find: %v\n%s", err, findJSON)
	}
	if len(found) != 1 || len(found[0].Results) != 1 ||
		found[0].Results[0].Path != "Service.swift" ||
		found[0].Results[0].Language != "swift" ||
		found[0].Results[0].Scope != "run" ||
		found[0].Results[0].StartLine != 5 || found[0].Results[0].EndLine != 7 {
		t.Fatalf("Swift find = %#v, want Target in run at 5-7", found)
	}

	inspectJSON := captureStdout(t, func() {
		if status := run([]string{
			"inspect", "Service.swift:6",
			"--root", root,
			"--include", "scope",
			"--return", "scope",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("inspect status = %d", status)
		}
	})
	var inspected navigator.InspectResponse
	if err := json.Unmarshal([]byte(inspectJSON), &inspected); err != nil {
		t.Fatalf("decode Swift inspect: %v\n%s", err, inspectJSON)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Language != "swift" || inspected.Results[0].Scope != "run" {
		t.Fatalf("Swift inspect = %#v, want Target in run", inspected)
	}
}

func TestOutlineBatchRetainsValidPathsWhenOneIsInvalid(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first.go":  "package demo\n\nfunc first() {}\n",
		"second.go": "package demo\n\nfunc second() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"outline",
			"first.go",
			"missing.go",
			"second.go",
			"--root", root,
			"--return", "line",
			"--limit", "3",
			"--json",
		})
	})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}

	var responses []navigator.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[2].Results) != 1 {
		t.Fatalf("valid results discarded: %#v", responses)
	}
	if responses[1].Path != "missing.go" ||
		!strings.Contains(responses[1].Error, "no such file") ||
		responses[1].Results == nil {
		t.Fatalf("invalid response = %#v", responses[1])
	}
}

func TestOutlineBatchFailsWhenEveryPathIsInvalid(t *testing.T) {
	root := t.TempDir()

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"outline",
			"missing.go",
			"also-missing.go",
			"--root", root,
			"--return", "line",
			"--limit", "2",
			"--json",
		})
	})
	if status == 0 {
		t.Fatal("expected all-invalid batch to fail")
	}

	var responses []navigator.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Error == "" || responses[1].Error == "" {
		t.Fatalf("responses = %#v", responses)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = previous
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}
