// Package projectcheck implements repository-wide CI policy checks in Go.
package projectcheck

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
	"go.yaml.in/yaml/v3"
)

// WorkflowShell is the only custom GitHub Actions shell template accepted by
// the repository policy. The Actions runner substitutes {0} with a temporary
// file that contains the step's run value; workflowrunner reads that file as
// data and never executes it.
const WorkflowShell = "go run -mod=readonly ./internal/cmd/workflow-runner -- {0}"

const taskctlLauncherExecutable = "/usr/local/libexec/scopesifter/taskctl-launcher"

var (
	makeTargetPattern  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	makeTokenPattern   = regexp.MustCompile(`^[A-Za-z0-9_./:=,+%-]+$`)
	makeIncludePattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.mk$`)
)

var approvedWorkflowActions = map[string][]map[string]string{
	"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1": {{
		"persist-credentials": "false",
	}},
	"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e": {{
		"cache":           "true",
		"go-version-file": "go.mod",
	}, {
		"cache":      "false",
		"go-version": "1.26.5",
	}},
	"actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6": {
		{"subject-checksums": "dist/SHA256SUMS"},
		{"subject-path": "dist/SHA256SUMS"},
	},
	"golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a": {{
		"args":    "--config=.golangci.yml",
		"version": "v2.12.2",
	}},
}

var approvedWorkflowTargets = map[string]struct{}{
	"ci-build":                    {},
	"ci-fieldalignment":           {},
	"ci-json":                     {},
	"ci-no-scripts":               {},
	"ci-test":                     {},
	"ci-vet":                      {},
	"release-artifacts":           {},
	"release-publish":             {},
	"tokenbench-privileged-linux": {},
}

type trackedFile struct {
	path string
	mode string
}

// ValidateJSON parses every tracked JSON file as exactly one JSON value.
func ValidateJSON(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, tracked := range paths {
		path := tracked.path
		if filepath.Ext(path) != ".json" {
			continue
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("open %s: %w", path, err))
			continue
		}
		decoder := json.NewDecoder(bufio.NewReader(file))
		var document any
		decodeErr := decoder.Decode(&document)
		if decodeErr == nil {
			var trailing any
			if err := decoder.Decode(&trailing); err == nil {
				decodeErr = errors.New("multiple JSON values")
			} else if !errors.Is(err, io.EOF) {
				decodeErr = fmt.Errorf("read trailing JSON: %w", err)
			}
		}
		closeErr := file.Close()
		if decodeErr != nil {
			failures = append(failures, fmt.Errorf("parse %s: %w", path, decodeErr))
		} else if closeErr != nil {
			failures = append(failures, fmt.Errorf("close %s: %w", path, closeErr))
		}
	}
	return errors.Join(failures...)
}

// ValidateWorkflowTarget reports whether target is an exact reviewed workflow
// target token.
func ValidateWorkflowTarget(target string) error {
	if _, approved := approvedWorkflowTargets[target]; !approved {
		return fmt.Errorf("workflow target %q is not approved", target)
	}
	return nil
}

// ValidateTrackedMakeTarget validates the complete tracked Make graph rooted
// at Makefile and reports whether target is both reviewed and defined by it.
func ValidateTrackedMakeTarget(root, target string) error {
	if err := ValidateWorkflowTarget(target); err != nil {
		return err
	}
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	targets, err := collectMakeTargets(root, paths)
	if err != nil {
		return err
	}
	if _, found := targets[target]; !found {
		return fmt.Errorf("unknown Make target %q", target)
	}
	return nil
}

// ValidateNoScripts rejects project-owned executable-script paths, script
// shebangs, and explicit script-runtime invocation. Go tests may contain inert
// language/provenance fixture bytes, but process calls are syntax-checked.
func ValidateNoScripts(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	makeTargets, err := collectMakeTargets(root, paths)
	if err != nil {
		return err
	}
	var failures []error
	for _, tracked := range paths {
		path := tracked.path
		if tracked.mode != "100644" {
			failures = append(failures, fmt.Errorf("tracked executable or special-mode path: %s (%s)", path, tracked.mode))
			continue
		}
		if isScriptAutomationConfig(path) {
			failures = append(failures, fmt.Errorf("tracked script-automation config: %s", path))
			continue
		}
		if isScriptPath(path) {
			failures = append(failures, fmt.Errorf("tracked script path: %s", path))
			continue
		}
		if isContainerBuildPath(path) {
			failures = append(failures, fmt.Errorf("tracked shell-capable container build file: %s", path))
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		if bytes.HasPrefix(data, []byte("#!")) {
			failures = append(failures, fmt.Errorf("project-owned shebang: %s", path))
			continue
		}
		if strings.HasPrefix(path, ".github/actions/") {
			failures = append(failures, fmt.Errorf("project-owned GitHub action is forbidden: %s", path))
			continue
		}
		if filepath.Ext(path) == ".go" {
			if err := rejectGoScriptExecution(path, data); err != nil {
				failures = append(failures, err)
			}
		}
		if isWorkflow(path) {
			if err := rejectWorkflowScripts(path, string(data), makeTargets); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func isScriptPath(path string) bool {
	return processpolicy.ProhibitedReference(path)
}

func isContainerBuildPath(path string) bool {
	base := filepath.Base(path)
	return base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") ||
		base == "Containerfile" || strings.HasPrefix(base, "Containerfile.")
}

func isScriptAutomationConfig(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "package.json", "deno.json", "deno.jsonc", "bunfig.toml",
		"Justfile", "justfile", "Rakefile", ".envrc", ".bashrc",
		".zshrc", ".profile", "Jenkinsfile", "Procfile":
		return true
	default:
		return strings.HasPrefix(base, "Taskfile.") &&
			(strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml"))
	}
}

func isMakefile(path string) bool {
	base := filepath.Base(path)
	return base == "Makefile" || strings.HasSuffix(base, ".mk")
}

func isWorkflow(path string) bool {
	return strings.HasPrefix(path, ".github/workflows/") &&
		(filepath.Ext(path) == ".yml" || filepath.Ext(path) == ".yaml")
}

type workflowDocument struct {
	Defaults    workflowDefaults       `yaml:"defaults"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
	Name        string                 `yaml:"name"`
	Permissions workflowPermissions    `yaml:"permissions"`
	Concurrency workflowConcurrency    `yaml:"concurrency"`
	On          workflowTriggers       `yaml:"on"`
}

type workflowTriggers struct {
	Push        workflowPushTrigger `yaml:"push"`
	PullRequest yaml.Node           `yaml:"pull_request"`
}

type workflowPushTrigger struct {
	Branches []string `yaml:"branches"`
	Tags     []string `yaml:"tags"`
}

type workflowPermissions struct {
	ArtifactMetadata string `yaml:"artifact-metadata"`
	Attestations     string `yaml:"attestations"`
	Contents         string `yaml:"contents"`
	IDToken          string `yaml:"id-token"`
}

type workflowConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

type workflowDefaults struct {
	Run workflowRunDefaults `yaml:"run"`
}

type workflowRunDefaults struct {
	Shell *string `yaml:"shell"`
}

type workflowJob struct {
	Permissions    workflowPermissions `yaml:"permissions"`
	Defaults       workflowDefaults    `yaml:"defaults"`
	Uses           *string             `yaml:"uses"`
	Container      string              `yaml:"container"`
	Environment    string              `yaml:"environment"`
	Name           string              `yaml:"name"`
	Needs          string              `yaml:"needs"`
	RunsOn         string              `yaml:"runs-on"`
	Steps          []workflowStep      `yaml:"steps"`
	TimeoutMinutes int                 `yaml:"timeout-minutes"`
}

type workflowStep struct {
	Run   *string           `yaml:"run"`
	Shell *string           `yaml:"shell"`
	Uses  *string           `yaml:"uses"`
	With  map[string]string `yaml:"with"`
	Env   map[string]string `yaml:"env"`
	Name  string            `yaml:"name"`
}

func reviewedReleaseWorkflowDocument() workflowDocument {
	workflowShell := WorkflowShell
	checkout := "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	setupGo := "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	attest := "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6"
	test := "ci-test"
	build := "release-artifacts"
	publish := "release-publish"
	return workflowDocument{
		Defaults: workflowDefaults{Run: workflowRunDefaults{Shell: &workflowShell}},
		Jobs: map[string]workflowJob{
			"test": {
				Name:           "Test",
				RunsOn:         "ubuntu-24.04",
				TimeoutMinutes: 20,
				Permissions:    workflowPermissions{Contents: "read"},
				Steps: []workflowStep{
					{
						Name: "Check out repository",
						Uses: &checkout,
						With: map[string]string{"persist-credentials": "false"},
					},
					{
						Name: "Set up Go",
						Uses: &setupGo,
						With: map[string]string{
							"cache":      "false",
							"go-version": "1.26.5",
						},
					},
					{Name: "Test", Run: &test},
				},
			},
			"release": {
				Name:           "Release",
				Needs:          "test",
				Environment:    "release",
				RunsOn:         "ubuntu-24.04",
				Container:      "golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f",
				TimeoutMinutes: 20,
				Permissions: workflowPermissions{
					ArtifactMetadata: "write",
					Attestations:     "write",
					Contents:         "read",
					IDToken:          "write",
				},
				Steps: []workflowStep{
					{
						Name: "Check out repository",
						Uses: &checkout,
						With: map[string]string{"persist-credentials": "false"},
					},
					{Name: "Build release archives", Run: &build},
					{
						Name: "Attest release artifacts",
						Uses: &attest,
						With: map[string]string{"subject-checksums": "dist/SHA256SUMS"},
					},
					{
						Name: "Attest release manifest",
						Uses: &attest,
						With: map[string]string{"subject-path": "dist/SHA256SUMS"},
					},
					{
						Name: "Publish GitHub release",
						Run:  &publish,
						Env: map[string]string{
							"GH_TOKEN": "${{ secrets.SCOPESIFTER_RELEASE_TOKEN }}",
						},
					},
				},
			},
		},
		Name:        "Release",
		Permissions: workflowPermissions{Contents: "read"},
		Concurrency: workflowConcurrency{Group: "release-${{ github.ref }}"},
		On: workflowTriggers{
			Push: workflowPushTrigger{Tags: []string{"v*"}},
		},
	}
}

func rejectWorkflowScripts(path, content string, makeTargets map[string]struct{}) error {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	decoder.KnownFields(true)
	var document workflowDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse workflow %s: %w", path, err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("workflow contains multiple YAML documents: %s", path)
		}
		return fmt.Errorf("parse trailing workflow YAML %s: %w", path, err)
	}
	if len(document.Jobs) == 0 {
		return fmt.Errorf("workflow has no jobs: %s", path)
	}
	var failures []error
	if err := validateWorkflowPermissions(path, document.Permissions); err != nil {
		failures = append(failures, err)
	}
	if err := validateWorkflowShell(path+" workflow default", document.Defaults.Run.Shell); err != nil {
		failures = append(failures, err)
	}
	if path == ".github/workflows/release.yml" &&
		!reflect.DeepEqual(document, reviewedReleaseWorkflowDocument()) {
		failures = append(failures, fmt.Errorf(
			"release workflow differs from the exact reviewed contract: %s",
			path,
		))
	}
	for jobName, job := range document.Jobs {
		if job.Uses != nil {
			failures = append(failures, fmt.Errorf("reusable workflow delegation is forbidden: %s job %s", path, jobName))
		}
		if err := validateWorkflowJobPermissions(path, jobName, job.Permissions); err != nil {
			failures = append(failures, err)
		}
		if err := validateWorkflowJobBoundary(path, jobName, job); err != nil {
			failures = append(failures, err)
		}
		if err := validateWorkflowShell(path+" job "+jobName+" default", job.Defaults.Run.Shell); err != nil {
			failures = append(failures, err)
		}
		if job.RunsOn != "ubuntu-latest" && job.RunsOn != "ubuntu-24.04" {
			failures = append(failures, fmt.Errorf("workflow job uses an unapproved runner: %s job %s", path, jobName))
		}
		if job.TimeoutMinutes < 1 || job.TimeoutMinutes > 45 {
			failures = append(failures, fmt.Errorf("workflow job has an invalid timeout: %s job %s", path, jobName))
		}
		for stepIndex, step := range job.Steps {
			location := fmt.Sprintf("%s job %s step %d", path, jobName, stepIndex+1)
			if step.Run != nil && step.Uses != nil {
				failures = append(failures, fmt.Errorf("workflow step mixes run and uses: %s", location))
			}
			if step.Uses != nil {
				if err := validateWorkflowAction(location, *step.Uses, step.With, step.Env); err != nil {
					failures = append(failures, err)
				}
			}
			if err := validateWorkflowShell(location, step.Shell); err != nil {
				failures = append(failures, err)
			}
			if step.Run == nil {
				if step.Shell != nil {
					failures = append(failures, fmt.Errorf("workflow action specifies a shell at %s", location))
				}
				continue
			}
			if len(step.With) != 0 {
				failures = append(failures, fmt.Errorf("workflow run step has action inputs at %s", location))
			}
			effectiveShell := step.Shell
			if effectiveShell == nil {
				effectiveShell = job.Defaults.Run.Shell
			}
			if effectiveShell == nil {
				effectiveShell = document.Defaults.Run.Shell
			}
			if effectiveShell == nil || *effectiveShell != WorkflowShell {
				failures = append(failures, fmt.Errorf("workflow run lacks the exact Go target runner: %s", location))
			}
			target, err := validateWorkflowMakeCommand(*step.Run, makeTargets)
			if err != nil {
				failures = append(failures, fmt.Errorf("workflow run command at %s: %w", location, err))
			} else if err := validateWorkflowRunEnvironment(target, step.Env); err != nil {
				failures = append(failures, fmt.Errorf("workflow run environment at %s: %w", location, err))
			}
		}
	}
	return errors.Join(failures...)
}

func validateWorkflowPermissions(path string, permissions workflowPermissions) error {
	// Empty permissions are accepted for isolated policy fixtures. Tracked
	// workflows otherwise use a read-only workflow-wide baseline; the release
	// job receives its complete elevated profile at job scope only.
	if permissions == (workflowPermissions{}) {
		return nil
	}
	if permissions == (workflowPermissions{Contents: "read"}) {
		return nil
	}
	return fmt.Errorf("workflow permissions differ from a complete reviewed profile: %s", path)
}

func validateWorkflowJobPermissions(
	path, jobName string,
	permissions workflowPermissions,
) error {
	if permissions == (workflowPermissions{}) ||
		permissions == (workflowPermissions{Contents: "read"}) {
		return nil
	}
	release := workflowPermissions{
		ArtifactMetadata: "write",
		Attestations:     "write",
		Contents:         "read",
		IDToken:          "write",
	}
	if path == ".github/workflows/release.yml" && jobName == "release" && permissions == release {
		return nil
	}
	return fmt.Errorf(
		"workflow job permissions differ from a complete reviewed profile: %s job %s",
		path,
		jobName,
	)
}

func validateWorkflowJobBoundary(path, jobName string, job workflowJob) error {
	const releaseContainer = "golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f"
	if job.Container == "" && job.Environment == "" && job.Needs == "" {
		return nil
	}
	if path == ".github/workflows/release.yml" && jobName == "release" &&
		job.Container == releaseContainer && job.Environment == "release" &&
		job.Needs == "test" {
		return nil
	}
	return fmt.Errorf("workflow job boundary differs from the reviewed profile: %s job %s", path, jobName)
}

func validateWorkflowAction(
	location, action string,
	inputs, environment map[string]string,
) error {
	expectedSets, approved := approvedWorkflowActions[action]
	if !approved {
		return fmt.Errorf("workflow action is local, unapproved, unpinned, or malformed at %s", location)
	}
	if len(environment) != 0 {
		return fmt.Errorf("workflow action environment is forbidden at %s", location)
	}
	for _, expected := range expectedSets {
		if reflect.DeepEqual(inputs, expected) {
			return nil
		}
	}
	return fmt.Errorf("workflow action input set differs from the reviewed sets at %s", location)
}

func validateWorkflowRunEnvironment(target string, environment map[string]string) error {
	expected := map[string]string{}
	switch target {
	case "tokenbench-privileged-linux":
		expected["TOKENBENCH_PRIVILEGED_IMAGE"] = "golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"
	case "release-publish":
		expected["GH_TOKEN"] = "${{ secrets.SCOPESIFTER_RELEASE_TOKEN }}"
	}
	if len(environment) != len(expected) {
		return errors.New("environment differs from the reviewed target-specific set")
	}
	for name, expectedValue := range expected {
		if environment[name] != expectedValue {
			return fmt.Errorf("environment value %s differs from the reviewed value", name)
		}
	}
	return nil
}

func validateWorkflowShell(location string, shell *string) error {
	if shell != nil && *shell != WorkflowShell {
		return fmt.Errorf("workflow shell is not the exact Go target runner at %s", location)
	}
	return nil
}

func validateWorkflowMakeCommand(command string, makeTargets map[string]struct{}) (string, error) {
	if err := ValidateWorkflowTarget(command); err != nil {
		return "", errors.New("must be one exact reviewed Make target token")
	}
	if _, found := makeTargets[command]; !found {
		return "", fmt.Errorf("unknown Make target %q", command)
	}
	return command, nil
}

func collectMakeTargets(root string, paths []trackedFile) (map[string]struct{}, error) {
	makefiles := make(map[string]makefileSpec)
	for _, tracked := range paths {
		if isImplicitMakeEntrypoint(tracked.path) {
			return nil, fmt.Errorf("tracked implicit Make entrypoint is forbidden: %s", tracked.path)
		}
		if !isMakefile(tracked.path) {
			continue
		}
		path := tracked.path
		if tracked.mode != "100644" {
			return nil, fmt.Errorf("tracked Make path must have mode 100644: %s (%s)", path, tracked.mode)
		}
		data, err := readRegularMakefile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read tracked Make path %s: %w", path, err)
		}
		spec, err := validateMakefile(path, string(data))
		if err != nil {
			return nil, err
		}
		makefiles[path] = spec
	}
	if _, ok := makefiles["Makefile"]; !ok {
		return nil, errors.New("tracked root Makefile is required")
	}
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var visit func(string) error
	visit = func(path string) error {
		if visiting[path] {
			return fmt.Errorf("make include cycle reaches %s", path)
		}
		if visited[path] {
			return nil
		}
		spec, ok := makefiles[path]
		if !ok {
			return fmt.Errorf("make include is not a tracked Makefile: %s", path)
		}
		visiting[path] = true
		for _, included := range spec.includes {
			if err := visit(included); err != nil {
				return err
			}
		}
		delete(visiting, path)
		visited[path] = true
		return nil
	}
	if err := visit("Makefile"); err != nil {
		return nil, err
	}
	if len(visited) != len(makefiles) {
		for path := range makefiles {
			if !visited[path] {
				return nil, fmt.Errorf("orphan tracked Makefile is forbidden: %s", path)
			}
		}
	}
	targets := make(map[string]struct{})
	phony := make(map[string]struct{})
	for path := range visited {
		spec := makefiles[path]
		for target, recipeCount := range spec.targets {
			if recipeCount == 0 {
				return nil, fmt.Errorf("make target has no direct recipe: %s in %s", target, path)
			}
			if _, duplicate := targets[target]; duplicate {
				return nil, fmt.Errorf("make target is defined more than once: %s", target)
			}
			targets[target] = struct{}{}
		}
		for target := range spec.phony {
			phony[target] = struct{}{}
		}
	}
	for target := range targets {
		if _, ok := phony[target]; !ok {
			return nil, fmt.Errorf("make target is not declared phony: %s", target)
		}
	}
	for target := range phony {
		if _, ok := targets[target]; !ok {
			return nil, fmt.Errorf("phony Make target is undefined: %s", target)
		}
	}
	return targets, nil
}

func readRegularMakefile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(statErr, file.Close())
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, errors.Join(errors.New("file changed while it was opened"), file.Close())
	}
	data, readErr := io.ReadAll(file)
	return data, errors.Join(readErr, file.Close())
}

func isImplicitMakeEntrypoint(path string) bool {
	base := filepath.Base(path)
	return base == "GNUmakefile" || base == "makefile"
}

func validMakeTargetName(target string) bool {
	return makeTargetPattern.MatchString(target)
}

type makefileSpec struct {
	targets  map[string]int
	phony    map[string]struct{}
	includes []string
}

func validateMakefile(path, content string) (makefileSpec, error) {
	spec := makefileSpec{targets: make(map[string]int), phony: make(map[string]struct{})}
	if strings.Contains(content, "\r") {
		return spec, fmt.Errorf("makefile contains carriage returns: %s", path)
	}
	currentTarget := ""
	for index, line := range strings.Split(content, "\n") {
		lineNumber := index + 1
		if strings.HasSuffix(line, `\`) {
			return spec, fmt.Errorf("make continuation is forbidden in %s:%d", path, lineNumber)
		}
		if strings.HasPrefix(line, "\t") {
			if currentTarget == "" {
				return spec, fmt.Errorf("orphan Make recipe in %s:%d", path, lineNumber)
			}
			if err := validateMakeRecipe(strings.TrimPrefix(line, "\t")); err != nil {
				return spec, fmt.Errorf("unsafe Make recipe in %s:%d: %w", path, lineNumber, err)
			}
			spec.targets[currentTarget]++
			continue
		}
		currentTarget = ""
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimSpace(line) != line {
			return spec, fmt.Errorf("unexpected Make indentation in %s:%d", path, lineNumber)
		}
		if strings.HasPrefix(line, "include ") {
			included := strings.TrimPrefix(line, "include ")
			if !makeIncludePattern.MatchString(included) ||
				!safeMakefilePath(included) || included == path {
				return spec, fmt.Errorf("unsafe Make include in %s:%d", path, lineNumber)
			}
			spec.includes = append(spec.includes, included)
			continue
		}
		if strings.HasPrefix(line, ".PHONY: ") {
			for _, target := range strings.Fields(strings.TrimPrefix(line, ".PHONY: ")) {
				if !validMakeTargetName(target) || strings.HasPrefix(target, ".") {
					return spec, fmt.Errorf("invalid phony target in %s:%d", path, lineNumber)
				}
				spec.phony[target] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(line, "export ") {
			for _, name := range strings.Fields(strings.TrimPrefix(line, "export ")) {
				if !validMakeEnvironmentName(name) {
					return spec, fmt.Errorf("unsafe Make export in %s:%d", path, lineNumber)
				}
			}
			continue
		}
		left, right, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(right) != "" || strings.ContainsAny(line, ";$`\\") {
			return spec, fmt.Errorf("unsupported Make syntax or prerequisite in %s:%d", path, lineNumber)
		}
		targetFields := strings.Fields(left)
		if len(targetFields) != 1 || !validMakeTargetName(targetFields[0]) || strings.HasPrefix(targetFields[0], ".") {
			return spec, fmt.Errorf("invalid Make target in %s:%d", path, lineNumber)
		}
		currentTarget = targetFields[0]
		if _, duplicate := spec.targets[currentTarget]; duplicate {
			return spec, fmt.Errorf("duplicate Make target in %s:%d", path, lineNumber)
		}
		spec.targets[currentTarget] = 0
	}
	return spec, nil
}

func safeMakefilePath(path string) bool {
	return path != "" && filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) == path &&
		!filepath.IsAbs(path) && path != ".." && !strings.HasPrefix(path, "../") && isMakefile(path)
}

func validMakeEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		if (character >= 'A' && character <= 'Z') || character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func validateMakeRecipe(recipe string) error {
	if recipe == "" || strings.TrimSpace(recipe) != recipe || strings.ContainsAny(recipe, "\t\r\n") {
		return errors.New("recipe must be one unindented command")
	}
	if strings.ContainsAny(recipe, "'\"`$\\;&|<>(){}[]*?!#") {
		return errors.New("shell syntax, expansion, quoting, and control operators are forbidden")
	}
	fields := strings.Split(recipe, " ")
	for _, field := range fields {
		if field == "" || !makeTokenPattern.MatchString(field) {
			return errors.New("recipe contains an unsafe command token")
		}
	}
	switch fields[0] {
	case "go":
		return validateMakeGoCommand(fields[1:])
	case taskctlLauncherExecutable:
		if !reviewedTaskctlLauncherRole(fields[1:]) {
			return errors.New("taskctl launcher arguments do not match a reviewed role")
		}
		return nil
	case "golangci-lint":
		if len(fields) != 3 || fields[1] != "run" ||
			(fields[2] != "--config=.golangci.yml" && fields[2] != "--config=.golangci-fieldalignment.yml") {
			return errors.New("golangci-lint recipe does not match a reviewed invocation")
		}
		return nil
	case "mkdir":
		if len(fields) != 3 || fields[1] != "-p" || fields[2] != "bin" {
			return errors.New("mkdir recipe does not match its reviewed fixed invocation")
		}
		return nil
	default:
		return fmt.Errorf("program %q is not an approved direct tool", fields[0])
	}
}

func validateMakeGoCommand(arguments []string) error {
	if len(arguments) < 2 {
		return errors.New("go recipe requires a reviewed subcommand and local package")
	}
	subcommand := arguments[0]
	arguments = arguments[1:]
	switch subcommand {
	case "run":
		if arguments[0] == "-mod=readonly" {
			arguments = arguments[1:]
		}
		if len(arguments) == 0 || !safeLocalGoPackage(arguments[0]) {
			return errors.New("go run requires one local package")
		}
		if !reviewedMakeGoRun(arguments[0], arguments[1:]) {
			return errors.New("go run program arguments do not match a reviewed role")
		}
		return nil
	case "test":
		packages := 0
		for _, argument := range arguments {
			if safeLocalGoPackage(argument) {
				packages++
				continue
			}
			if argument != "-count=1" {
				return fmt.Errorf("unreviewed go test argument %q", argument)
			}
		}
		if packages == 0 {
			return errors.New("go test requires a local package")
		}
		return nil
	case "vet":
		for _, argument := range arguments {
			if !safeLocalGoPackage(argument) {
				return fmt.Errorf("unreviewed go vet argument %q", argument)
			}
		}
		return nil
	case "build":
		packageCount := 0
		for index := 0; index < len(arguments); index++ {
			argument := arguments[index]
			switch {
			case argument == "-trimpath":
			case argument == "-o" && index+1 < len(arguments):
				index++
				if !safeMakeOutputPath(arguments[index]) {
					return errors.New("go build output path is unsafe")
				}
			case safeLocalGoPackage(argument):
				packageCount++
			default:
				return fmt.Errorf("unreviewed go build argument %q", argument)
			}
		}
		if packageCount == 0 {
			return errors.New("go build requires a local package")
		}
		return nil
	default:
		return fmt.Errorf("go subcommand %q is not approved", subcommand)
	}
}

func reviewedMakeGoRun(packagePath string, arguments []string) bool {
	roles := map[string][][]string{
		"./benchmarks/tokenbench/cmd/privileged-linux-tests": {nil},
		"./internal/cmd/grammar-generator": {
			{"-repo", "."},
			{"-language", "csharp", "-repo", "."},
			{"-language", "kotlin", "-repo", "."},
			{"-language", "swift", "-repo", "."},
		},
		"./internal/cmd/project-check": {
			{"-mode", "json"},
			{"-mode", "no-scripts"},
		},
		"./internal/cmd/release-artifacts": {
			{"-mode", "build"},
			{"-mode", "publish"},
		},
	}
	for _, role := range roles[packagePath] {
		if slices.Equal(arguments, role) {
			return true
		}
	}
	return false
}

func reviewedTaskctlLauncherRole(arguments []string) bool {
	roles := [][2]string{
		{"inspect", "executable-sha256"},
		{"generate", "source-audit"},
		{"generate", "source-repository-bindings"},
		{"generate", "source-selections"},
		{"validate", "source-audit"},
		{"validate", "source-repository-bindings"},
		{"validate", "source-selections"},
	}
	if len(arguments) != 2 {
		return false
	}
	for _, role := range roles {
		if arguments[0] == role[0] && arguments[1] == role[1] {
			return true
		}
	}
	return false
}

func safeLocalGoPackage(value string) bool {
	if !strings.HasPrefix(value, "./") || strings.Contains(value, "@") {
		return false
	}
	trimmed := strings.TrimPrefix(value, "./")
	return trimmed != "" && trimmed != "." && trimmed != ".." &&
		!strings.HasPrefix(trimmed, "../") &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(trimmed))) == trimmed
}

func safeMakeOutputPath(value string) bool {
	return value != "" && !filepath.IsAbs(value) && !strings.Contains(value, "@") &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value &&
		value != ".." && !strings.HasPrefix(value, "../") &&
		!isScriptRuntimeWord(value)
}

func isScriptRuntimeWord(word string) bool {
	base := commandBase(word)
	return base == "busybox" || processpolicy.ProhibitedReference(base)
}

func commandBase(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	base := strings.ToLower(path.Base(normalized))
	return strings.TrimSuffix(base, ".exe")
}

type goProcessImports struct {
	aliases map[string]string
	dot     map[string]struct{}
}

// astObject is limited to parser-created single-file bindings. This checker
// deliberately does not use these links for type-dependent selector or
// composite-literal resolution.
type astObject = ast.Object //nolint:staticcheck // The parser binding is sufficient for this conservative scan.

func collectGoProcessImports(file *ast.File) goProcessImports {
	imports := goProcessImports{
		aliases: make(map[string]string),
		dot:     make(map[string]struct{}),
	}
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil || !isProcessPackage(importPath) {
			continue
		}
		name := path.Base(importPath)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		switch name {
		case ".":
			imports.dot[importPath] = struct{}{}
		case "_":
			continue
		default:
			imports.aliases[name] = importPath
		}
	}
	return imports
}

func isProcessPackage(importPath string) bool {
	switch importPath {
	case "os", "os/exec", "syscall", "golang.org/x/sys/execabs", "golang.org/x/sys/unix",
		"github.com/yapless/scopesifter/internal/processpolicy":
		return true
	default:
		return false
	}
}

func processCommandIndex(importPath, function string) (int, bool) {
	switch importPath {
	case "os/exec":
		switch function {
		case "Command":
			return 0, true
		case "CommandContext":
			return 1, true
		}
	case "golang.org/x/sys/execabs":
		switch function {
		case "Command":
			return 0, true
		case "CommandContext":
			return 1, true
		}
	case "os":
		if function == "StartProcess" {
			return 0, true
		}
	case "syscall":
		switch function {
		case "Exec", "ForkExec", "StartProcess":
			return 0, true
		}
	case "golang.org/x/sys/unix":
		switch function {
		case "Exec", "ForkExec":
			return 0, true
		}
	case "github.com/yapless/scopesifter/internal/processpolicy":
		switch function {
		case "NativeCommand":
			return 0, true
		case "NativeCommandContext":
			return 1, true
		}
	}
	return 0, false
}

func processCallCommandIndex(function ast.Expr, imports goProcessImports) (int, bool) {
	switch function := function.(type) {
	case *ast.ParenExpr:
		return processCallCommandIndex(function.X, imports)
	case *ast.SelectorExpr:
		identifier, ok := function.X.(*ast.Ident)
		if !ok || identifier.Obj != nil {
			return 0, false
		}
		return processCommandIndex(imports.aliases[identifier.Name], function.Sel.Name)
	case *ast.Ident:
		// An identifier introduced by a dot import has no local ast.Object.
		// Do not mistake a locally declared function with the same name for an
		// imported process primitive.
		if function.Obj != nil {
			return 0, false
		}
		for importPath := range imports.dot {
			if commandIndex, ok := processCommandIndex(importPath, function.Name); ok {
				return commandIndex, true
			}
		}
	}
	return 0, false
}

func isProcessFunctionValue(expression ast.Expr, imports goProcessImports) bool {
	_, ok := processCallCommandIndex(expression, imports)
	return ok
}

func containsScriptRuntimeToken(value string) bool {
	if isScriptRuntimeWord(value) {
		return true
	}
	for _, field := range strings.Fields(value) {
		field = strings.Trim(field, "'\"`(){}[];,|&!")
		if _, assigned, found := strings.Cut(field, "="); found {
			field = assigned
		}
		field = strings.Trim(field, "'\"`(){}[];,|&!")
		if isScriptRuntimeWord(field) {
			return true
		}
	}
	return false
}

func collectValueBindings(file *ast.File) map[*astObject][]ast.Expr {
	bindings := make(map[*astObject][]ast.Expr)
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.GenDecl)
		if !ok || (declaration.Tok != token.CONST && declaration.Tok != token.VAR) {
			return true
		}
		var previous []ast.Expr
		for _, rawSpec := range declaration.Specs {
			spec, ok := rawSpec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			values := spec.Values
			if len(values) == 0 && declaration.Tok == token.CONST {
				values = previous
			} else if len(values) != 0 && declaration.Tok == token.CONST {
				previous = values
			}
			if len(values) != len(spec.Names) {
				continue
			}
			for index, name := range spec.Names {
				if name.Obj != nil {
					bindings[name.Obj] = append(bindings[name.Obj], values[index])
				}
			}
		}
		return false
	})
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
			return true
		}
		for index, rawName := range assignment.Lhs {
			name, ok := rawName.(*ast.Ident)
			if ok && name.Obj != nil {
				bindings[name.Obj] = append(bindings[name.Obj], assignment.Rhs[index])
			}
		}
		return true
	})
	return bindings
}

func rejectGoScriptExecution(path string, data []byte) error {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse Go source %s: %w", path, err)
	}
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:generate") {
				return fmt.Errorf("go generation directive is forbidden in %s", path)
			}
		}
	}
	for _, imported := range parsed.Imports {
		importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
		if unquoteErr == nil && importPath == "net/http/cgi" &&
			!approvedDynamicProcessFile(path, data) {
			return fmt.Errorf("unapproved process-capable Go package in %s", path)
		}
	}
	imports := collectGoProcessImports(parsed)
	bindings := collectValueBindings(parsed)
	allowDynamicProcess := approvedDynamicProcessFile(path, data)
	directProcessFunctions := make(map[ast.Expr]struct{})
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		function := unwrapParenthesizedExpression(call.Fun)
		if _, ok := processCallCommandIndex(function, imports); ok {
			directProcessFunctions[function] = struct{}{}
		}
		return true
	})
	var found bool
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			commandIndex, ok := processCallCommandIndex(node.Fun, imports)
			if ok && len(node.Args) > commandIndex &&
				processInvocationRunsScript(
					node.Args,
					commandIndex,
					bindings,
					allowDynamicProcess,
				) {
				found = true
				return false
			}
		case *ast.AssignStmt:
			if !allowDynamicProcess && assignmentMutatesProcessCommand(node) {
				found = true
				return false
			}
			for _, expression := range node.Rhs {
				if !allowDynamicProcess && isProcessFunctionValue(expression, imports) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			if !allowDynamicProcess && node.Type != nil && isExecCmdType(node.Type, imports) {
				found = true
				return false
			}
			for _, expression := range node.Values {
				if !allowDynamicProcess && isProcessFunctionValue(expression, imports) {
					found = true
					return false
				}
			}
		case *ast.TypeSpec:
			if !allowDynamicProcess && isExecCmdType(node.Type, imports) {
				found = true
				return false
			}
		case *ast.CompositeLit:
			if execCmdLiteralRunsScript(node, imports, bindings) {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if !allowDynamicProcess && isExecCmdType(node, imports) {
				found = true
				return false
			}
			if isProcessFunctionValue(node, imports) {
				if _, direct := directProcessFunctions[node]; !direct && !allowDynamicProcess {
					found = true
					return false
				}
			}
		case *ast.Ident:
			if !allowDynamicProcess && isExecCmdType(node, imports) {
				found = true
				return false
			}
			if isProcessFunctionValue(node, imports) {
				if _, direct := directProcessFunctions[node]; !direct && !allowDynamicProcess {
					found = true
					return false
				}
			}
		}
		return true
	})
	if found {
		return fmt.Errorf("script-runtime process execution in %s", path)
	}
	return nil
}

func assignmentMutatesProcessCommand(assignment *ast.AssignStmt) bool {
	for _, expression := range assignment.Lhs {
		selector, ok := expression.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		switch selector.Sel.Name {
		case "Path", "Args":
			return true
		}
	}
	return false
}

func unwrapParenthesizedExpression(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func processInvocationRunsScript(
	arguments []ast.Expr,
	commandIndex int,
	bindings map[*astObject][]ast.Expr,
	allowDynamic bool,
) bool {
	if expressionContainsScriptRuntime(arguments[commandIndex], bindings) {
		return true
	}
	command, ok := constantString(arguments[commandIndex], bindings)
	if !ok {
		return !allowDynamic
	}
	if isScriptRuntimeWord(command) {
		return true
	}
	for _, argument := range arguments[commandIndex+1:] {
		if expressionContainsScriptRuntime(argument, bindings) {
			return true
		}
		if _, constant := constantString(argument, bindings); !constant && !allowDynamic {
			return true
		}
	}
	return false
}

func approvedDynamicProcessFile(path string, data []byte) bool {
	expected, approved := approvedDynamicProcessFileSHA256[path]
	if !approved {
		return false
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)) == expected
}

func expressionContainsScriptRuntime(
	expression ast.Expr,
	bindings map[*astObject][]ast.Expr,
) bool {
	return expressionContainsScriptRuntimeResolving(
		expression,
		bindings,
		make(map[*astObject]struct{}),
	)
}

func expressionContainsScriptRuntimeResolving(
	expression ast.Expr,
	bindings map[*astObject][]ast.Expr,
	resolving map[*astObject]struct{},
) bool {
	if identifier, ok := expression.(*ast.Ident); ok && identifier.Obj != nil {
		if _, active := resolving[identifier.Obj]; !active {
			resolving[identifier.Obj] = struct{}{}
			for _, binding := range bindings[identifier.Obj] {
				if expressionContainsScriptRuntimeResolving(binding, bindings, resolving) {
					delete(resolving, identifier.Obj)
					return true
				}
			}
			delete(resolving, identifier.Obj)
		}
	}
	var found bool
	ast.Inspect(expression, func(node ast.Node) bool {
		if found {
			return false
		}
		expression, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		value, ok := constantString(expression, bindings)
		if !ok {
			return true
		}
		if containsScriptRuntimeToken(value) {
			found = true
		}
		return false
	})
	return found
}

func execCmdLiteralRunsScript(
	literal *ast.CompositeLit,
	imports goProcessImports,
	bindings map[*astObject][]ast.Expr,
) bool {
	if !isExecCmdType(literal.Type, imports) {
		return false
	}
	var command ast.Expr
	var arguments ast.Expr
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch name.Name {
		case "Path":
			command = field.Value
		case "Args":
			arguments = field.Value
		}
	}
	if command == nil {
		return false
	}
	if expressionContainsScriptRuntime(command, bindings) {
		return true
	}
	commandValue, ok := constantString(command, bindings)
	if !ok {
		return false
	}
	if isScriptRuntimeWord(commandValue) {
		return true
	}
	return arguments != nil && expressionContainsScriptRuntime(arguments, bindings)
}

func isExecCmdType(expression ast.Expr, imports goProcessImports) bool {
	switch expression := expression.(type) {
	case *ast.SelectorExpr:
		identifier, ok := expression.X.(*ast.Ident)
		return ok && identifier.Obj == nil && imports.aliases[identifier.Name] == "os/exec" &&
			expression.Sel.Name == "Cmd"
	case *ast.Ident:
		if expression.Obj != nil || expression.Name != "Cmd" {
			return false
		}
		_, ok := imports.dot["os/exec"]
		return ok
	default:
		return false
	}
}

func constantString(expression ast.Expr, bindings map[*astObject][]ast.Expr) (string, bool) {
	return constantStringResolving(expression, bindings, make(map[*astObject]struct{}))
}

func constantStringResolving(
	expression ast.Expr,
	bindings map[*astObject][]ast.Expr,
	resolving map[*astObject]struct{},
) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		result, err := strconv.Unquote(value.Value)
		return result, err == nil
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := constantStringResolving(value.X, bindings, resolving)
		right, rightOK := constantStringResolving(value.Y, bindings, resolving)
		return left + right, leftOK && rightOK
	case *ast.ParenExpr:
		return constantStringResolving(value.X, bindings, resolving)
	case *ast.Ident:
		if value.Obj == nil || value.Obj.Kind != ast.Con {
			return "", false
		}
		if _, active := resolving[value.Obj]; active {
			return "", false
		}
		expressions := bindings[value.Obj]
		if len(expressions) != 1 {
			return "", false
		}
		resolving[value.Obj] = struct{}{}
		result, ok := constantStringResolving(expressions[0], bindings, resolving)
		delete(resolving, value.Obj)
		return result, ok
	default:
		return "", false
	}
}

func trackedFiles(root string) ([]trackedFile, error) {
	arguments := append(gitdiffcontract.InvocationPrefix(), "ls-files", "--stage", "-z")
	if err := processpolicy.ValidateGit(arguments...); err != nil {
		return nil, fmt.Errorf("validate tracked-file Git invocation: %w", err)
	}
	command, gitFile, err := processpolicy.NativeCommand("git", arguments...)
	if err != nil {
		return nil, fmt.Errorf("pin native Git: %w", err)
	}
	command.Dir = root
	command.Env = gitdiffcontract.Environment(os.DevNull)
	output, err := command.Output()
	closeErr := gitFile.Close()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close tracked-file Git image: %w", closeErr)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]trackedFile, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		metadata, rawPath, found := bytes.Cut(part, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 || string(fields[2]) != "0" {
			return nil, fmt.Errorf("malformed tracked index entry")
		}
		path := string(rawPath)
		if filepath.IsAbs(path) || filepath.Clean(path) != filepath.FromSlash(path) {
			return nil, fmt.Errorf("unsafe tracked path: %q", path)
		}
		paths = append(paths, trackedFile{path: path, mode: string(fields[0])})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].path < paths[j].path })
	return paths, nil
}
