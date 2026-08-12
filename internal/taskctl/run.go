// Package taskctl implements typed, script-free generation and validation of
// benchmark task artifacts.
package taskctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const usage = `usage:
  taskctl generate source-audit --git-executable PATH --git-sha256 SHA256 --repository-bindings FILE --repository-bindings-sha256 SHA256 --source-selections FILE --source-selections-sha256 SHA256 --output FILE
  taskctl generate source-repository-bindings --git-executable PATH --git-sha256 SHA256 --repository UPSTREAM=PATH [...] --output FILE
  taskctl generate source-selections --input FILE --input-sha256 SHA256 --output FILE
  taskctl validate source-audit --git-executable PATH --git-sha256 SHA256 --repository-bindings FILE --repository-bindings-sha256 SHA256 --source-selections FILE --source-selections-sha256 SHA256 --input FILE
  taskctl validate source-repository-bindings --git-executable PATH --git-sha256 SHA256 --input FILE --input-sha256 SHA256
  taskctl validate source-selections --input FILE --input-sha256 SHA256

All generate --output paths are create-only and must not already exist.
`

// Run executes one taskctl operation and returns a process exit code.
func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "taskctl: %v\n", err)
		return 1
	}
	if len(arguments) < 2 {
		if _, err := fmt.Fprint(stderr, usage); err != nil {
			return 1
		}
		return 2
	}

	verb, role := arguments[0], arguments[1]
	if err := validateTaskctlScalarEnvironments(verb, role, arguments[2:]); err != nil {
		fmt.Fprintf(stderr, "taskctl: %v\n", err)
		return 1
	}
	var err error
	switch {
	case verb == "generate" && role == "source-audit":
		err = runGenerateSourceAudit(ctx, arguments[2:], stderr)
	case verb == "generate" && role == "source-repository-bindings":
		err = runGenerateSourceRepositoryBindings(ctx, arguments[2:], stderr)
	case verb == "generate" && role == "source-selections":
		err = runGenerateSourceSelections(arguments[2:], stderr)
	case verb == "validate" && role == "source-audit":
		err = runValidateSourceAudit(ctx, arguments[2:], stdout, stderr)
	case verb == "validate" && role == "source-repository-bindings":
		err = runValidateSourceRepositoryBindings(ctx, arguments[2:], stdout, stderr)
	case verb == "validate" && role == "source-selections":
		err = runValidateSourceSelections(arguments[2:], stdout, stderr)
	default:
		if _, err := fmt.Fprint(stderr, usage); err != nil {
			return 1
		}
		return 2
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintf(stderr, "taskctl: %v\n", err)
	return 1
}

type sourceAuditFlags struct {
	repositoryBindings       string
	repositoryBindingsSHA256 string
	sourceSelections         string
	sourceSelectionsSHA256   string
	gitExecutable            string
	gitSHA256                string
}

func registerSourceAuditFlags(flags *flag.FlagSet) *sourceAuditFlags {
	options := &sourceAuditFlags{}
	flags.StringVar(&options.repositoryBindings, "repository-bindings", taskctlEnvironmentString(taskctlRepositoryBindingsEnvironment), "authenticated source-repository bindings")
	flags.StringVar(&options.repositoryBindingsSHA256, "repository-bindings-sha256", taskctlEnvironmentString(taskctlRepositoryBindingsSHA256Environment), "independently supplied repository-binding SHA-256")
	flags.StringVar(&options.sourceSelections, "source-selections", taskctlEnvironmentString(taskctlSourceSelectionsEnvironment), "canonical 144-record source-selection manifest")
	flags.StringVar(&options.sourceSelectionsSHA256, "source-selections-sha256", taskctlEnvironmentString(taskctlSourceSelectionsSHA256Environment), "independently supplied source-selection SHA-256")
	flags.StringVar(&options.gitExecutable, "git-executable", taskctlEnvironmentString(taskctlGitExecutableEnvironment), "independently supplied canonical native-Git executable")
	flags.StringVar(&options.gitSHA256, "git-sha256", taskctlEnvironmentString(taskctlGitSHA256Environment), "independently supplied native-Git SHA-256")
	return options
}

func (options sourceAuditFlags) complete() bool {
	return options.repositoryBindings != "" && options.repositoryBindingsSHA256 != "" &&
		options.sourceSelections != "" && options.sourceSelectionsSHA256 != "" &&
		options.gitExecutable != "" && options.gitSHA256 != ""
}

func (options sourceAuditFlags) buildOptions() SourceAuditOptions {
	return SourceAuditOptions{
		RepositoryBindings:       options.repositoryBindings,
		RepositoryBindingsSHA256: options.repositoryBindingsSHA256,
		SourceSelections:         options.sourceSelections,
		SourceSelectionsSHA256:   options.sourceSelectionsSHA256,
		GitExecutable:            options.gitExecutable,
		GitSHA256:                options.gitSHA256,
	}
}

func runGenerateSourceAudit(ctx context.Context, arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("generate source-audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := registerSourceAuditFlags(flags)
	output := flags.String("output", taskctlEnvironmentString(taskctlOutputEnvironment), "new canonical source-audit report (must not exist)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !options.complete() || *output == "" {
		return errors.New("generate source-audit requires --git-executable, --git-sha256, --repository-bindings, --repository-bindings-sha256, --source-selections, --source-selections-sha256, and --output")
	}
	initialOutputParent, err := captureAtomicPublicationParent(*output)
	if err != nil {
		return err
	}
	prepared, err := prepareSourceAudit(ctx, options.buildOptions())
	if err != nil {
		return err
	}
	publication, err := captureSourceAuditPublication(prepared, *output)
	if err != nil {
		return fmt.Errorf("validate source-audit publication: %w", err)
	}
	if !os.SameFile(initialOutputParent, publication.outputParent) ||
		initialOutputParent.Mode() != publication.outputParent.Mode() {
		return errors.New("source-audit output directory changed during preparation")
	}
	publication.outputParent = initialOutputParent
	report, err := buildPreparedSourceAudit(ctx, prepared)
	if err != nil {
		return err
	}
	if err := prepared.revalidate(ctx); err != nil {
		return fmt.Errorf("revalidate prepared source audit before publication: %w", err)
	}
	if err := publication.revalidate(); err != nil {
		return fmt.Errorf("revalidate source-audit publication: %w", err)
	}
	if err := writeAtomicWithParent(
		publication.output,
		report,
		publication.outputParent,
	); err != nil {
		return fmt.Errorf("write source-audit report: %w", err)
	}
	return nil
}

func runValidateSourceAudit(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate source-audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := registerSourceAuditFlags(flags)
	input := flags.String("input", taskctlEnvironmentString(taskctlInputEnvironment), "canonical source-audit report")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !options.complete() || *input == "" {
		return errors.New("validate source-audit requires --git-executable, --git-sha256, --repository-bindings, --repository-bindings-sha256, --source-selections, --source-selections-sha256, and --input")
	}
	expected, err := BuildSourceAudit(ctx, options.buildOptions())
	if err != nil {
		return err
	}
	actual, err := readRegularFile(*input)
	if err != nil {
		return fmt.Errorf("read source-audit report: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("source-audit report is not the canonical generated bytes")
	}
	_, err = fmt.Fprintln(stdout, "PASS canonical 144-cell source audit")
	return err
}

type repeatableStrings []string

func (values *repeatableStrings) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatableStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runGenerateSourceRepositoryBindings(
	ctx context.Context,
	arguments []string,
	stderr io.Writer,
) error {
	flags := flag.NewFlagSet("generate source-repository-bindings", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var repositories repeatableStrings
	flags.Var(&repositories, "repository", "required upstream=canonical-absolute-path binding")
	gitExecutable := flags.String("git-executable", taskctlEnvironmentString(taskctlGitExecutableEnvironment), "required canonical absolute native-Git executable")
	gitSHA256 := flags.String("git-sha256", taskctlEnvironmentString(taskctlGitSHA256Environment), "required lowercase SHA-256 of native Git")
	output := flags.String("output", taskctlEnvironmentString(taskctlOutputEnvironment), "new canonical source-repository binding document (must not exist)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	inputs, err := resolveTaskctlRepositoryEnvironment(repositories)
	if err != nil {
		return err
	}
	if flags.NArg() != 0 || len(inputs) == 0 || *gitExecutable == "" ||
		*gitSHA256 == "" || *output == "" {
		return errors.New("generate source-repository-bindings requires --git-executable, --git-sha256, exactly 12 --repository UPSTREAM=PATH flags or TASKCTL_REPOSITORIES_JSON records, and --output")
	}
	publicationInputs := make([]publicationInputPath, 0, len(inputs)+1)
	publicationInputs = append(publicationInputs, publicationInputPath{
		label: "Git executable", path: *gitExecutable,
	})
	for _, input := range inputs {
		publicationInputs = append(publicationInputs, publicationInputPath{
			label: "repository " + input.Upstream, path: input.Path, directory: true,
		})
	}
	publication, err := capturePublicationPath(*output, publicationInputs...)
	if err != nil {
		return fmt.Errorf("validate source-repository binding publication: %w", err)
	}
	encoded, err := BuildSourceAuditRepositoryBindings(
		ctx,
		inputs,
		*gitExecutable,
		*gitSHA256,
	)
	if err != nil {
		return err
	}
	if err := publication.revalidate(); err != nil {
		return fmt.Errorf("revalidate source-repository binding publication: %w", err)
	}
	if err := writeAtomicWithParent(
		publication.output,
		encoded,
		publication.outputParent,
	); err != nil {
		return fmt.Errorf("write source-repository bindings: %w", err)
	}
	return nil
}

func runValidateSourceRepositoryBindings(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("validate source-repository-bindings", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", taskctlEnvironmentString(taskctlInputEnvironment), "canonical source-repository binding document")
	inputSHA256 := flags.String("input-sha256", taskctlEnvironmentString(taskctlInputSHA256Environment), "independently supplied binding-document SHA-256")
	gitExecutable := flags.String("git-executable", taskctlEnvironmentString(taskctlGitExecutableEnvironment), "independently supplied canonical native-Git executable")
	gitSHA256 := flags.String("git-sha256", taskctlEnvironmentString(taskctlGitSHA256Environment), "independently supplied native-Git SHA-256")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *inputSHA256 == "" ||
		*gitExecutable == "" || *gitSHA256 == "" {
		return errors.New("validate source-repository-bindings requires --git-executable, --git-sha256, --input, and --input-sha256")
	}
	data, err := readAuthenticatedTaskctlInput(*input, *inputSHA256, "repository bindings")
	if err != nil {
		return err
	}
	if _, err := sourceAuditRepositoryBindingsFromBytes(ctx, data, sourceAuditGitIdentity{
		executable: *gitExecutable,
		sha256:     *gitSHA256,
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "PASS canonical current source-repository bindings")
	return err
}

func runValidateSourceSelections(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate source-selections", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", taskctlEnvironmentString(taskctlInputEnvironment), "canonical 144-record source-selection manifest")
	inputSHA256 := flags.String("input-sha256", taskctlEnvironmentString(taskctlInputSHA256Environment), "independently supplied manifest SHA-256")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *inputSHA256 == "" {
		return errors.New("validate source-selections requires --input and --input-sha256")
	}
	data, err := readAuthenticatedTaskctlInput(*input, *inputSHA256, "source selections")
	if err != nil {
		return err
	}
	if _, err := ValidateSourceSelections(data); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "PASS canonical 144-record source selections")
	return err
}

func runGenerateSourceSelections(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("generate source-selections", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", taskctlEnvironmentString(taskctlInputEnvironment), "authenticated source-selection authoring input")
	inputSHA256 := flags.String("input-sha256", taskctlEnvironmentString(taskctlInputSHA256Environment), "independently supplied authoring-input SHA-256")
	output := flags.String("output", taskctlEnvironmentString(taskctlOutputEnvironment), "new canonical 144-record source-selection manifest (must not exist)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *inputSHA256 == "" || *output == "" {
		return errors.New("generate source-selections requires --input, --input-sha256, and --output")
	}
	publication, err := capturePublicationPath(
		*output,
		publicationInputPath{label: "source-selection authoring input", path: *input},
	)
	if err != nil {
		return fmt.Errorf("validate source-selection publication: %w", err)
	}
	data, err := readAuthenticatedTaskctlInput(*input, *inputSHA256, "source-selection authoring input")
	if err != nil {
		return err
	}
	if err := publication.revalidate(); err != nil {
		return fmt.Errorf("revalidate source-selection input: %w", err)
	}
	encoded, err := GenerateSourceSelections(data)
	if err != nil {
		return err
	}
	if err := publication.revalidate(); err != nil {
		return fmt.Errorf("revalidate source-selection publication: %w", err)
	}
	if err := writeAtomicWithParent(publication.output, encoded, publication.outputParent); err != nil {
		return fmt.Errorf("write source selections: %w", err)
	}
	return nil
}

func readAuthenticatedTaskctlInput(path, expectedSHA256, label string) ([]byte, error) {
	if !sourceAuditDigest.MatchString(expectedSHA256) {
		return nil, fmt.Errorf("%s SHA-256 must be lowercase 64-hex", label)
	}
	data, err := readRegularFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != expectedSHA256 {
		return nil, fmt.Errorf("%s SHA-256 is %s, want %s", label, actual, expectedSHA256)
	}
	return data, nil
}

func captureSourceAuditPublication(
	prepared *preparedSourceAudit,
	output string,
) (*publicationPathSnapshot, error) {
	if prepared == nil || prepared.inputs == nil || prepared.gitInfo == nil {
		return nil, errors.New("prepared source-audit snapshot is incomplete")
	}
	bindingPath, bindingInfo, err := prepared.inputs.identityFor(
		prepared.options.RepositoryBindings,
	)
	if err != nil {
		return nil, fmt.Errorf("identify repository bindings for publication safety: %w", err)
	}
	selectionPath, selectionInfo, err := prepared.inputs.identityFor(
		prepared.options.SourceSelections,
	)
	if err != nil {
		return nil, fmt.Errorf("identify source selections for publication safety: %w", err)
	}
	inputs := []publicationInputPath{
		{label: "repository bindings", path: bindingPath, expected: bindingInfo},
		{label: "source selections", path: selectionPath, expected: selectionInfo},
		{
			label: "Git executable", path: prepared.expectedGit.executable,
			expected: prepared.gitInfo,
		},
	}
	for _, repository := range sourceAuditRepositories {
		path, pathFound := prepared.repositoryBindings.paths[repository.upstream]
		info, infoFound := prepared.repositoryBindings.pathInfos[repository.upstream]
		if !pathFound || !infoFound || info == nil {
			return nil, fmt.Errorf(
				"prepared source-audit repository %s is incomplete",
				repository.upstream,
			)
		}
		inputs = append(inputs, publicationInputPath{
			label: "repository " + repository.upstream,
			path:  path, directory: true, expected: info,
		})
	}
	return capturePublicationPath(output, inputs...)
}

func writeAtomicWithParent(
	path string,
	data []byte,
	expectedParent os.FileInfo,
) error {
	if expectedParent == nil {
		return errors.New("expected atomic publication parent identity is missing")
	}
	return writeAtomicPinned(path, data, atomicPublicationHooks{
		expectedParent: expectedParent,
	})
}

func captureAtomicPublicationParent(path string) (os.FileInfo, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make atomic publication path absolute: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return nil, errors.New(
			"atomic publication output already exists; create-only publication did not modify it",
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect create-only atomic publication output: %w", err)
	}
	_, info, err := inspectCanonicalPublicationPath(filepath.Dir(absolute), true)
	if err != nil {
		return nil, fmt.Errorf("capture atomic publication parent: %w", err)
	}
	return info, nil
}
