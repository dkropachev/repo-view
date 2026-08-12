package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/cas"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/evidence"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	harnesscodex "github.com/yapless/scopesifter/benchmarks/tokenbench/harness/codex"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/runner"
	runnercodex "github.com/yapless/scopesifter/benchmarks/tokenbench/runner/codex"
	executionsnapshot "github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
)

const (
	verifyResultSchema = "tokenbench.verify-result/v2"
	runResultSchema    = "tokenbench.run-result/v2"
	replayResultSchema = "tokenbench.replay-result/v2"
	closeTimeout       = 15 * time.Second
	maxCASObjectBytes  = int64(harness.MaxArtifactBytes)
)

func validateCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "path to a built-in Codex suite JSON document")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" || flags.NArg() != 0 {
		return errors.New("validate requires exactly --suite PATH")
	}
	loaded, err := tokenbench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	if err := requireCodexSuite(loaded.Suite()); err != nil {
		return err
	}
	offlineRoot := filepath.Join(
		filepath.Dir(loaded.Path()),
		".tokenbench-offline-validation-runtime",
	)
	adapter, err := offlineCodexAdapter(offlineRoot)
	if err != nil {
		return err
	}
	if _, err := tokenbench.PrepareSuite(ctx, loaded, adapter); err != nil {
		return fmt.Errorf("prepare built-in Codex suite: %w", err)
	}
	suite := loaded.Suite()
	return writeJSON(stdout, struct {
		SchemaVersion string `json:"schema_version"`
		SuiteID       string `json:"suite_id"`
		SuiteSHA256   string `json:"suite_sha256"`
		PromptSHA256  string `json:"prompt_sha256"`
		HarnessKind   string `json:"harness_kind"`
		Repetitions   int    `json:"repetitions"`
	}{
		SchemaVersion: tokenbench.SuiteSchemaVersion,
		SuiteID:       suite.ID,
		SuiteSHA256:   loaded.Digest(),
		PromptSHA256:  loaded.PromptDigest(),
		HarnessKind:   suite.HarnessKind,
		Repetitions:   suite.Repetitions,
	})
}

func planCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "path to a built-in Codex suite JSON document")
	scopeSifterPath := flags.String("scopesifter-mcp", "", "absolute scopesifter MCP executable path")
	stateRoot := flags.String("state-root", "", "absolute runtime root represented in this audit plan")
	outputPath := flags.String("out", "-", "new exclusive plan path, or - for stdout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" || *scopeSifterPath == "" || *stateRoot == "" || flags.NArg() != 0 {
		return errors.New(
			"plan requires --suite PATH, --scopesifter-mcp PATH, and --state-root PATH",
		)
	}
	root, err := requireAbsoluteClean(*stateRoot, "plan state root")
	if err != nil {
		return err
	}
	loaded, err := tokenbench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	if err := requireCodexSuite(loaded.Suite()); err != nil {
		return err
	}
	adapter, err := offlineCodexAdapter(root)
	if err != nil {
		return err
	}
	pair, err := prepareCodexPair(ctx, loaded, adapter, *scopeSifterPath)
	if err != nil {
		return err
	}
	plan, err := pair.Plan(ctx)
	if err != nil {
		return fmt.Errorf("build verified Codex audit plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if *outputPath == "-" {
		return writeJSON(stdout, plan)
	}
	return writeJSONFileExclusive(*outputPath, plan, 0o644, "plan")
}

func runCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (resultErr error) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "path to the authored built-in Codex suite")
	artifactBundleRoot := flags.String(
		"artifact-bundle",
		"",
		"absolute root of the suite/binary-pinned static execution artifact bundle",
	)
	snapshotRoot := flags.String(
		"snapshot-root",
		"",
		"absent path for the immutable fs-verity execution snapshot",
	)
	stateRoot := flags.String("state-root", "", "new exclusive private Codex runtime directory")
	casPath := flags.String("cas", "", "new exclusive evidence CAS directory")
	rootOutput := flags.String("root-out", "", "new exclusive canonical signed-root file")
	var credentialFD secretDescriptor
	flags.Var(&credentialFD, "credential-fd", "canonical inherited secret-source descriptor in [3,255], closed before launch")
	signingKeyPath := flags.String("signing-key-file", "", "owner-only file containing one canonical Ed25519 seed")
	trustPath := flags.String("trust-policy", "", "explicit policy authorizing the capture signer")
	repetition := flags.Int("repetition", -1, "one explicit zero-based suite repetition to execute")
	privateMountChild := flags.Bool(
		"private-mount-child",
		false,
		"internal: continue only inside tokenbench's private mount namespace",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" || *artifactBundleRoot == "" || *snapshotRoot == "" || *stateRoot == "" ||
		*casPath == "" || *rootOutput == "" || !credentialFD.set ||
		*signingKeyPath == "" || *trustPath == "" || *repetition < 0 || flags.NArg() != 0 {
		return errors.New(
			"run requires --suite, --artifact-bundle, --snapshot-root, --state-root, --cas, " +
				"--root-out, --credential-fd, --signing-key-file, --trust-policy, and an explicit " +
				"nonnegative --repetition",
		)
	}
	// This must precede any credential-source read. NewProduction repeats the
	// check while snapshotting the system trust roots.
	if err := runnercodex.ValidateProductionEnvironment(); err != nil {
		return err
	}
	loaded, err := tokenbench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	suite := loaded.Suite()
	if err := requireCodexSuite(suite); err != nil {
		return err
	}
	if *repetition >= suite.Repetitions {
		return fmt.Errorf(
			"selected repetition %d is outside the suite's explicit [0,%d) range",
			*repetition,
			suite.Repetitions,
		)
	}
	paths, err := resolveRunPaths(runPaths{
		StateRoot:          *stateRoot,
		SnapshotRoot:       *snapshotRoot,
		ArtifactBundleRoot: *artifactBundleRoot,
		CAS:                *casPath,
		RootOutput:         *rootOutput,
		SigningKeyFile:     *signingKeyPath,
		TrustPolicy:        *trustPath,
		SourceRoot:         suite.SourceRoot,
	})
	if err != nil {
		return err
	}
	if !*privateMountChild {
		return reexecRunInPrivateMountNamespace(
			ctx,
			args,
			credentialFD.value,
			stdout,
			stderr,
		)
	}
	if err := enterPrivateMountNamespaceChild(); err != nil {
		return err
	}
	bundle, err := tokenbench.LoadArtifactBundle(paths.ArtifactBundleRoot, loaded)
	if err != nil {
		return fmt.Errorf("load trusted execution artifact bundle: %w", err)
	}
	origins, err := tokenbench.PrepareOrigins(ctx, loaded, bundle)
	if err != nil {
		return fmt.Errorf("prepare immutable execution origins: %w", err)
	}
	preparedExecution, err := tokenbench.BuildExecutionSnapshot(
		ctx,
		origins,
		paths.SnapshotRoot,
	)
	if err != nil {
		return fmt.Errorf("build immutable execution snapshot: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, preparedExecution.Close())
	}()
	executionInputs, err := preparedExecution.Inputs(ctx)
	if err != nil {
		return fmt.Errorf("read immutable execution inputs: %w", err)
	}
	credential, err := loadCredentialFD(credentialFD.value)
	if err != nil {
		return err
	}
	defer clear(credential)
	if err := claimPrivateDirectory(paths.StateRoot, "Codex state root"); err != nil {
		return err
	}
	if err := claimPrivateDirectory(paths.CAS, "evidence CAS"); err != nil {
		return err
	}
	store, err := cas.Open(paths.CAS, cas.Options{MaxObjectBytes: maxCASObjectBytes})
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			resultErr = errors.Join(resultErr, store.Close())
		}
	}()
	output, err := claimExclusiveOutput(paths.RootOutput, 0o600, "signed root")
	if err != nil {
		return err
	}
	defer output.abort()

	var publication evidence.PublicationResult
	var publicationErr error
	publicationAttempted := false
	err = afterClosedCodexExecution(
		func() (tokenbench.Run, error) {
			return executeCodexPair(
				ctx,
				loaded,
				&preparedExecution,
				executionInputs,
				paths.StateRoot,
				credential,
				*repetition,
			)
		},
		func(run tokenbench.Run) error {
			// executeCodexPair returns a run only after Executor.Close and then
			// Lifecycle.Close both succeeded. The policy is explicit and this is
			// the first private-key read.
			verifier, err := loadTrustPolicy(paths.TrustPolicy)
			if err != nil {
				return err
			}
			signer, err := loadEd25519Signer(paths.SigningKeyFile)
			if err != nil {
				return err
			}
			publicationAttempted = true
			publication, publicationErr = evidence.PublishRun(
				ctx, store, run, signer, verifier,
			)
			return nil
		},
	)
	if err != nil {
		return err
	}
	if !publicationAttempted {
		return errors.New("capture publication was not attempted")
	}
	closeErr := store.Close()
	store = nil
	publicationCause := errors.Join(
		publicationErr,
		publicationCleanupWarning(publication),
		closeErr,
	)
	if !finalizablePublication(publication) {
		return finalizeIncompletePublication(
			paths.RootOutput,
			evidence.CaptureBundle,
			publication,
			publicationCause,
		)
	}
	if err := finalizeCompletePublication(
		output, paths.RootOutput, evidence.CaptureBundle, publication, publicationCause,
	); err != nil {
		return err
	}
	root := publication.IntendedRoot
	return writeJSON(stdout, struct {
		SchemaVersion    string        `json:"schema_version"`
		Root             cas.ObjectRef `json:"root"`
		BundleKind       string        `json:"bundle_kind"`
		Selected         int           `json:"selected_repetition"`
		SuiteRepetitions int           `json:"suite_repetitions"`
	}{
		SchemaVersion:    runResultSchema,
		Root:             root,
		BundleKind:       string(evidence.CaptureBundle),
		Selected:         *repetition,
		SuiteRepetitions: suite.Repetitions,
	})
}

func verifyCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (resultErr error) {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casPath := flags.String("cas", "", "existing evidence CAS directory")
	rootPath := flags.String("root", "", "canonical signed-root reference file")
	trustPath := flags.String("trust-policy", "", "explicit canonical out-of-band trust policy")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *casPath == "" || *rootPath == "" || *trustPath == "" || flags.NArg() != 0 {
		return errors.New("verify requires --cas PATH, --root PATH, and --trust-policy PATH")
	}
	verifiedPaths, err := canonicalCommandPaths([]namedPath{
		{"evidence CAS", *casPath},
		{"signed root reference", *rootPath},
		{"trust policy", *trustPath},
	})
	if err != nil {
		return err
	}
	root, err := readCanonicalRoot(verifiedPaths[1].path)
	if err != nil {
		return err
	}
	verifier, err := loadTrustPolicy(verifiedPaths[2].path)
	if err != nil {
		return err
	}
	store, err := openCAS(verifiedPaths[0].path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	verified, err := evidence.VerifyEvidence(ctx, store, root, verifier)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		SchemaVersion     string              `json:"schema_version"`
		Root              cas.ObjectRef       `json:"root"`
		Subject           cas.ObjectRef       `json:"subject"`
		BundleKind        evidence.BundleKind `json:"bundle_kind"`
		KeyID             string              `json:"key_id"`
		TrustPolicySHA256 string              `json:"trust_policy_sha256"`
		Parents           []cas.ObjectRef     `json:"parents"`
	}{
		SchemaVersion:     verifyResultSchema,
		Root:              verified.Root,
		Subject:           verified.Subject,
		BundleKind:        verified.BundleKind,
		KeyID:             verified.KeyID,
		TrustPolicySHA256: verified.TrustPolicySHA256,
		Parents:           verified.Parents,
	})
}

func replayCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (resultErr error) {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casPath := flags.String("cas", "", "existing evidence CAS containing the capture")
	rootPath := flags.String("root", "", "canonical signed capture-root reference file")
	trustPath := flags.String("trust-policy", "", "explicit canonical out-of-band trust policy")
	signingKeyPath := flags.String("signing-key-file", "", "owner-only file containing one canonical Ed25519 seed")
	rootOutput := flags.String("root-out", "", "new exclusive canonical replay-root file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *casPath == "" || *rootPath == "" || *trustPath == "" ||
		*signingKeyPath == "" || *rootOutput == "" || flags.NArg() != 0 {
		return errors.New(
			"replay requires --cas, --root, --trust-policy, --signing-key-file, and --root-out",
		)
	}
	replayPaths, err := canonicalCommandPaths([]namedPath{
		{"evidence CAS", *casPath},
		{"capture root reference", *rootPath},
		{"trust policy", *trustPath},
		{"signing key file", *signingKeyPath},
		{"replay root output", *rootOutput},
		{"publication recovery output", *rootOutput + ".recovery.json"},
	})
	if err != nil {
		return err
	}
	root, err := readCanonicalRoot(replayPaths[1].path)
	if err != nil {
		return err
	}
	verifier, err := loadTrustPolicy(replayPaths[2].path)
	if err != nil {
		return err
	}
	store, err := openCAS(replayPaths[0].path)
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			resultErr = errors.Join(resultErr, store.Close())
		}
	}()
	verifiedParent, err := evidence.VerifyEvidence(ctx, store, root, verifier)
	if err != nil {
		return err
	}
	if verifiedParent.Capture == nil || verifiedParent.BundleKind != evidence.CaptureBundle {
		return errors.New("replay root must authenticate an original capture")
	}
	capture := verifiedParent.Capture
	adapter, err := harnesscodex.AdapterForCanonicalProcess(
		capture.Run.Plan.Baseline,
		capture.Run.Plan.RenderedProcesses.Baseline,
	)
	if err != nil {
		return fmt.Errorf("reconstruct built-in Codex replay adapter: %w", err)
	}
	decoder, err := evidence.NewCodexDecoder(adapter)
	if err != nil {
		return err
	}
	sourceRoot := capture.Run.Plan.Baseline.WorkingDirectory
	if pathsOverlap(replayPaths[3].path, sourceRoot) ||
		pathsOverlap(replayPaths[4].path, sourceRoot) {
		return errors.New("replay signing key and output must be disjoint from the captured source")
	}
	output, err := claimExclusiveOutput(replayPaths[4].path, 0o600, "replay root")
	if err != nil {
		return err
	}
	defer output.abort()
	// Replay is offline and starts no lifecycle or model process. The signer is
	// still loaded only after the complete parent graph and decoder binding are
	// authenticated.
	signer, err := loadEd25519Signer(replayPaths[3].path)
	if err != nil {
		return err
	}
	publication, publicationErr := evidence.ReplayCapture(
		ctx,
		store,
		root,
		verifier,
		decoder,
		signer,
	)
	closeErr := store.Close()
	store = nil
	publicationCause := errors.Join(
		publicationErr,
		publicationCleanupWarning(publication),
		closeErr,
	)
	if !finalizablePublication(publication) {
		return finalizeIncompletePublication(
			replayPaths[4].path,
			evidence.ReplayBundle,
			publication,
			publicationCause,
		)
	}
	if err := finalizeCompletePublication(
		output,
		replayPaths[4].path,
		evidence.ReplayBundle,
		publication,
		publicationCause,
	); err != nil {
		return err
	}
	replayRoot := publication.IntendedRoot
	return writeJSON(stdout, struct {
		SchemaVersion string        `json:"schema_version"`
		Root          cas.ObjectRef `json:"root"`
		Parent        cas.ObjectRef `json:"parent"`
		BundleKind    string        `json:"bundle_kind"`
	}{
		SchemaVersion: replayResultSchema,
		Root:          replayRoot,
		Parent:        root,
		BundleKind:    string(evidence.ReplayBundle),
	})
}

func publicationCleanupWarning(result evidence.PublicationResult) error {
	if !result.CleanupWarning {
		return nil
	}
	return errors.New("evidence publication completed with pending staging cleanup")
}

func requireCodexSuite(suite tokenbench.Suite) error {
	if suite.HarnessKind != "codex" {
		return fmt.Errorf(
			"harness_kind %q is not publishable; this command accepts only the built-in Codex adapter",
			suite.HarnessKind,
		)
	}
	return nil
}

func offlineCodexAdapter(stateRoot string) (*harnesscodex.Adapter, error) {
	root, err := requireAbsoluteClean(stateRoot, "offline Codex state root")
	if err != nil {
		return nil, err
	}
	return harnesscodex.New(harnesscodex.RuntimeLayout{
		ProxyURL:             "http://127.0.0.1:1/v1",
		Home:                 filepath.Join(root, "home"),
		CodexHome:            filepath.Join(root, "codex-home"),
		Temp:                 filepath.Join(root, "tmp"),
		ConfigLock:           filepath.Join(root, "config-lock"),
		ToolboxRoot:          root + "-toolbox",
		LocalProxyCapability: harnesscodex.OfflineLocalProxyCapability,
	})
}

func prepareCodexPair(
	ctx context.Context,
	loaded tokenbench.LoadedSuite,
	adapter *harnesscodex.Adapter,
	scopeSifterPath string,
) (tokenbench.Pair, error) {
	prepared, err := tokenbench.PrepareSuite(ctx, loaded, adapter)
	if err != nil {
		return tokenbench.Pair{}, fmt.Errorf("prepare built-in Codex suite: %w", err)
	}
	scopeSifterAbsolute, err := filepath.Abs(scopeSifterPath)
	if err != nil {
		return tokenbench.Pair{}, fmt.Errorf("resolve scopesifter MCP executable: %w", err)
	}
	scopeSifterAbsolute = filepath.Clean(scopeSifterAbsolute)
	tool, err := tokenbench.NewScopeSifterTool(scopeSifterAbsolute)
	if err != nil {
		return tokenbench.Pair{}, err
	}
	pair, err := tokenbench.ResolvePair(prepared, tool)
	if err != nil {
		return tokenbench.Pair{}, err
	}
	return pair, nil
}

func executeCodexPair(
	ctx context.Context,
	loaded tokenbench.LoadedSuite,
	prepared *tokenbench.PreparedExecution,
	executionInputs executionsnapshot.ExecutionInputs,
	stateRoot string,
	credential []byte,
	repetition int,
) (tokenbench.Run, error) {
	if prepared == nil {
		return tokenbench.Run{}, errors.New("live immutable execution preparation is required")
	}
	liveInputs, err := prepared.Inputs(ctx)
	if err != nil || !reflect.DeepEqual(liveInputs, executionInputs) {
		return tokenbench.Run{}, errors.Join(
			errors.New("immutable execution inputs changed before lifecycle construction"),
			err,
		)
	}
	suite := loaded.Suite()
	lifecycle, err := runnercodex.NewProduction(runnercodex.ProductionConfig{
		StateRoot:          stateRoot,
		ToolboxRoot:        executionInputs.ToolboxRoot,
		UpstreamCredential: string(credential),
		UpstreamTimeout:    time.Duration(suite.TimeoutMillis) * time.Millisecond,
	})
	clear(credential)
	if err != nil {
		return tokenbench.Run{}, err
	}
	closeLifecycleOnError := func(cause error) (tokenbench.Run, error) {
		return tokenbench.Run{}, errors.Join(cause, closeOne(lifecycle.Close))
	}
	layout := lifecycle.RuntimeLayout()
	adapter, err := harnesscodex.NewProduction(harnesscodex.RuntimeLayout{
		ProxyURL:             layout.ProxyURL,
		Home:                 layout.Home,
		CodexHome:            layout.CodexHome,
		Temp:                 layout.Temp,
		ConfigLock:           layout.ConfigLock,
		ToolboxRoot:          layout.ToolboxRoot,
		LocalProxyCapability: layout.LocalProxyCapability,
	})
	if err != nil {
		return closeLifecycleOnError(err)
	}
	pair, err := tokenbench.BindAdapter(ctx, prepared, adapter)
	if err != nil {
		return closeLifecycleOnError(err)
	}
	closePairOnError := func(cause error) (tokenbench.Run, error) {
		return tokenbench.Run{}, errors.Join(
			cause,
			closeOne(lifecycle.Close),
			pair.CloseExecutionSnapshot(),
		)
	}
	scopeSifterSHA256, err := tokenbench.FileSHA256(executionInputs.ScopeSifterExecutable)
	if err != nil {
		return closePairOnError(fmt.Errorf("pin common scopesifter executable: %w", err))
	}
	executor, err := runner.NewConformant(runner.Config{
		Lifecycle:                 lifecycle,
		ReadOnlyPaths:             executionInputs.ReadOnlyPaths,
		ExecutablePaths:           executionInputs.ExecutablePaths,
		CommonMCPExecutable:       executionInputs.ScopeSifterExecutable,
		CommonMCPExecutableSHA256: scopeSifterSHA256,
	})
	if err != nil {
		return closePairOnError(err)
	}
	run, executionErr := pair.Execute(ctx, executor, repetition)
	cleanupErr := closeExecutionBoundary(
		executor.Close,
		lifecycle.Close,
		pair.CloseExecutionSnapshot,
	)
	if err := errors.Join(executionErr, cleanupErr); err != nil {
		return run, err
	}
	return run, nil
}

type closeFunction func(context.Context) error
type snapshotCloseFunction func() error

func afterClosedCodexExecution(
	execute func() (tokenbench.Run, error),
	loadSignerAndPublish func(tokenbench.Run) error,
) error {
	run, err := execute()
	if err != nil {
		return err
	}
	return loadSignerAndPublish(run)
}

func closeExecutionBoundary(
	executorClose, lifecycleClose closeFunction,
	snapshotClose snapshotCloseFunction,
) error {
	// Ordering is security-sensitive: contained model descendants first, then
	// the lifecycle-owned proxy/listener/state, and finally the immutable mount
	// and inode pins. Every close is attempted and signing is gated on a nil
	// joined result plus the live publication-boundary predicates.
	executorErr := closeOne(executorClose)
	lifecycleErr := closeOne(lifecycleClose)
	var snapshotErr error
	if snapshotClose != nil {
		snapshotErr = snapshotClose()
	}
	return errors.Join(executorErr, lifecycleErr, snapshotErr)
}

func closeOne(closeFn closeFunction) error {
	if closeFn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return closeFn(ctx)
}

func writeJSONFileExclusive(path string, value any, mode os.FileMode, label string) error {
	var encoded bytes.Buffer
	if err := writeJSON(&encoded, value); err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	output, err := claimExclusiveOutput(path, mode, label)
	if err != nil {
		return err
	}
	defer output.abort()
	return output.commit(encoded.Bytes())
}
