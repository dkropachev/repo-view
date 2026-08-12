// Package runner provides the code-owned, shell-free process executor used by
// tokenbench. It is harness-neutral; harness-specific wire capture is supplied
// through a pinned Lifecycle implementation.
package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/internal/commandrunner"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

const (
	executorVersion        = "tokenbench.process-executor/v3"
	defaultMaxOutput       = harness.MaxRawStreamBytes
	defaultMaxArtifact     = harness.MaxArtifactBytes
	defaultWaitDelay       = 500 * time.Millisecond
	maximumWaitDelay       = 5 * time.Second
	defaultCleanupTimeout  = 5 * time.Second
	minimumLifecycleIDSize = 16
)

// ArmSession owns all state initialized for one arm. Finish captures sanitized
// artifacts. Abort is idempotent and must clean up even after a partial or
// cancelled arm.
type ArmSession interface {
	Finish(context.Context, ExecutionRequest, harness.RawExecution) ([]harness.Artifact, error)
	Abort(context.Context) error
}

// ArmNetworkPolicy supplies the exact TCP ports created for one arm. Landlock
// denies every connect and bind omitted from these lists. Conformant Codex
// sessions must implement this capability.
type ArmNetworkPolicy interface {
	AllowedConnectTCPPorts() []uint16
	AllowedBindTCPPorts() []uint16
}

// Lifecycle resets arm-local state and starts capture infrastructure without
// mutating the approved launch specification. If BeginArm returns both a
// session and an error, the runner still aborts that partial session. Identity
// commits all behavior/static configuration, never a live credential.
type Lifecycle interface {
	Identity() string
	BeginArm(context.Context, ExecutionRequest) (ArmSession, error)
}

// Config controls bounded execution. Zero values select conservative defaults.
type Config struct {
	Lifecycle                 Lifecycle
	CommonMCPExecutableSHA256 string
	CommonMCPExecutable       string
	WritablePaths             []string
	ExecutablePaths           []string
	ReadOnlyPaths             []string
	MaxArtifactBytes          int
	CleanupTimeout            time.Duration
	WaitDelay                 time.Duration
	MaxStderrBytes            int
	MaxStdoutBytes            int
	RequireContainment        bool
	allowUnboundedContainment bool
}

// Executor executes an argv ProcessSpec with its complete environment.
type Executor struct {
	lifecycle            Lifecycle
	containment          *cgroupManager
	devNullRule          *os.File
	commonSlot           *os.File
	commonMCP            *pinnedCommonExecutable
	active               map[*preparedExecution]struct{}
	armInit              *pinnedCommonExecutable
	identity             ExecutorIdentity
	lifecycleIdentity    string
	construction         constructionMode
	executablePaths      []string
	readOnlyPaths        []string
	writablePaths        []string
	cleanup              time.Duration
	maxStdout            int
	landlockABI          int
	waitDelay            time.Duration
	maxArtifact          int
	maxStderr            int
	mu                   sync.Mutex
	closeMu              sync.Mutex
	fullyClosed          bool
	closed               bool
	fullFilesystemPolicy bool
	exactNetworkBoundary bool
	pidNamespace         bool
}

// New validates and commits an extension-friendly executor configuration. An
// executor built with New is deliberately non-publishable regardless of its
// lifecycle; callers cannot self-attest a generic extension as conformant.
func New(config Config) (*Executor, error) {
	return newExecutor(config, genericConstruction)
}

// NewConformant validates and commits the sole publishable construction: the
// exact code-owned Codex v0.144.0 lifecycle. Concrete package identity and its
// pinned lifecycle identity are checked before provenance is granted.
func NewConformant(config Config) (*Executor, error) {
	if !exactCodexLifecycle(config.Lifecycle) {
		return nil, errors.New("conformant runner construction requires the built-in Codex lifecycle")
	}
	return newExecutor(config, conformantCodexConstruction)
}

func newExecutor(config Config, construction constructionMode) (_ *Executor, resultErr error) {
	if !processContainmentSupported() || runtime.GOOS != "linux" {
		return nil, errors.New("runner process-tree containment is unsupported on this platform")
	}
	maxStdout := config.MaxStdoutBytes
	if maxStdout == 0 {
		maxStdout = defaultMaxOutput
	}
	maxStderr := config.MaxStderrBytes
	if maxStderr == 0 {
		maxStderr = defaultMaxOutput
	}
	maxArtifact := config.MaxArtifactBytes
	if maxArtifact == 0 {
		maxArtifact = defaultMaxArtifact
	}
	waitDelay := config.WaitDelay
	if waitDelay == 0 {
		waitDelay = defaultWaitDelay
	}
	cleanupTimeout := config.CleanupTimeout
	if cleanupTimeout == 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	if maxStdout <= 0 || maxStderr <= 0 || maxArtifact <= 0 {
		return nil, errors.New("runner byte limits must be positive")
	}
	if maxStdout > harness.MaxRawStreamBytes || maxStderr > harness.MaxRawStreamBytes {
		return nil, errors.New("runner stream byte limit exceeds the raw-capture schema limit")
	}
	if maxArtifact > harness.MaxArtifactBytes {
		return nil, errors.New("runner artifact byte limit exceeds the artifact schema limit")
	}
	if waitDelay <= 0 || waitDelay > maximumWaitDelay || cleanupTimeout <= 0 ||
		cleanupTimeout > defaultCleanupTimeout {
		return nil, errors.New("runner wait delay or cleanup timeout is outside its supported bound")
	}
	lifecycleIdentity := "none"
	if config.Lifecycle != nil {
		lifecycleIdentity = config.Lifecycle.Identity()
		if len(lifecycleIdentity) < minimumLifecycleIDSize {
			return nil, errors.New("runner lifecycle identity is missing or too short")
		}
	}
	writablePaths := config.WritablePaths
	if construction == conformantCodexConstruction {
		if len(config.WritablePaths) != 0 {
			return nil, errors.New("conformant runner derives writable paths from the built-in lifecycle")
		}
		filesystemLifecycle, ok := config.Lifecycle.(interface {
			FilesystemWritePaths() []string
		})
		if !ok {
			return nil, errors.New("built-in lifecycle omitted its filesystem write policy")
		}
		writablePaths = filesystemLifecycle.FilesystemWritePaths()
	}
	writablePaths, err := normalizeWritablePaths(writablePaths)
	if err != nil {
		return nil, err
	}
	readOnlyPaths, err := normalizePolicyPaths(config.ReadOnlyPaths, "read-only")
	if err != nil {
		return nil, err
	}
	executablePaths, err := normalizePolicyPaths(config.ExecutablePaths, "executable")
	if err != nil {
		return nil, err
	}
	if err := validateFilesystemPolicySeparation(
		writablePaths,
		readOnlyPaths,
		executablePaths,
	); err != nil {
		return nil, err
	}
	fullFilesystemPolicy := len(readOnlyPaths) != 0 && len(executablePaths) != 0
	requireContainment := construction == conformantCodexConstruction || config.RequireContainment
	var containment *cgroupManager
	if requireContainment || config.allowUnboundedContainment {
		var err error
		containment, err = discoverCgroupManager(
			cleanupTimeout,
			requireContainment && !config.allowUnboundedContainment,
		)
		if err != nil {
			return nil, fmt.Errorf("initialize runner cgroup containment: %w", err)
		}
		defer func() {
			if resultErr != nil {
				resultErr = errors.Join(resultErr, containment.close())
			}
		}()
	}
	if construction == conformantCodexConstruction && !fullFilesystemPolicy {
		return nil, errors.New(
			"conformant runner requires complete Landlock read-only and executable path allowlists",
		)
	}
	var armInit *pinnedCommonExecutable
	var commonMCP *pinnedCommonExecutable
	var commonSlot *os.File
	var devNullRule *os.File
	landlockVersion := 0
	// Every cgroup-contained arm needs its own PID namespace. When namespace init
	// exits, kernel namespace teardown terminates and releases its remaining
	// descendants, so a detached child cannot remain a zombie charged to the arm
	// merely because an outer host or container PID 1 does not reap adopted
	// processes. Publishable construction already required this boundary;
	// generic contained execution must provide the same cleanup invariant.
	pidNamespace := containment != nil
	exactNetworkBoundary := false
	if containment != nil {
		requireStatic := construction == conformantCodexConstruction &&
			!config.allowUnboundedContainment
		armInit, landlockVersion, err = prepareArmInit(requireStatic)
		if err != nil {
			return nil, fmt.Errorf("initialize runner arm-init: %w", err)
		}
		defer func() {
			if resultErr != nil && armInit != nil {
				resultErr = errors.Join(resultErr, armInit.close())
			}
		}()
		switch {
		case config.CommonMCPExecutable == "" && config.CommonMCPExecutableSHA256 == "":
			if construction == conformantCodexConstruction {
				return nil, errors.New("conformant runner requires the common scopesifter FD5 executable")
			}
			commonSlot, err = os.Open(os.DevNull)
		case config.CommonMCPExecutable == "" ||
			config.CommonMCPExecutableSHA256 == "" ||
			!validSHA256(config.CommonMCPExecutableSHA256):
			return nil, errors.New("common scopesifter executable path and SHA-256 must be complete")
		default:
			commonMCP, err = pinExecutable(
				config.CommonMCPExecutable,
				config.CommonMCPExecutableSHA256,
				requireStatic,
				false,
				false,
			)
			if err == nil {
				commonSlot = commonMCP.launchFile
			}
		}
		if err != nil {
			return nil, fmt.Errorf("pin common scopesifter executable: %w", err)
		}
		devNullRule, err = openDevNullRule()
		if err != nil {
			return nil, fmt.Errorf("pin arm-init /dev/null rule: %w", err)
		}
		defer func() {
			if resultErr != nil {
				if commonMCP != nil {
					resultErr = errors.Join(resultErr, commonMCP.close())
				} else if commonSlot != nil {
					resultErr = errors.Join(resultErr, commonSlot.Close())
				}
				if devNullRule != nil {
					resultErr = errors.Join(resultErr, devNullRule.Close())
				}
			}
		}()
		if err := probeArmInitBoundary(
			containment,
			armInit.launchFile,
			commonSlot,
			devNullRule,
			cleanupTimeout,
			pidNamespace,
		); err != nil {
			return nil, fmt.Errorf("prove runner arm-init boundary: %w", err)
		}
		if construction == conformantCodexConstruction {
			if err := probeExactConnectPolicy(containment, cleanupTimeout); err != nil {
				return nil, fmt.Errorf("prove exact loopback cgroup connect boundary: %w", err)
			}
			exactNetworkBoundary = true
		}
	} else if len(writablePaths) != 0 || len(readOnlyPaths) != 0 ||
		len(executablePaths) != 0 || config.CommonMCPExecutable != "" ||
		config.CommonMCPExecutableSHA256 != "" {
		return nil, errors.New("landlock and common FD5 policy require cgroup containment")
	}
	canonical := struct { //nolint:govet,nolintlint // Field order defines the v3 configuration hash.
		Version              string   `json:"version"`
		Construction         string   `json:"construction"`
		LifecycleIdentity    string   `json:"lifecycle_identity"`
		MaxStdoutBytes       int      `json:"max_stdout_bytes"`
		MaxStderrBytes       int      `json:"max_stderr_bytes"`
		MaxArtifactBytes     int      `json:"max_artifact_bytes"`
		WaitDelayNanos       int64    `json:"wait_delay_nanos"`
		CleanupNanos         int64    `json:"cleanup_timeout_nanos"`
		Containment          any      `json:"containment"`
		NetworkPolicy        any      `json:"network_policy"`
		ArmInit              any      `json:"arm_init"`
		WritablePaths        []string `json:"writable_paths"`
		ReadOnlyPaths        []string `json:"read_only_paths"`
		ExecutablePaths      []string `json:"executable_paths"`
		FullFilesystemPolicy bool     `json:"full_filesystem_policy"`
		CommonMCPFD          int      `json:"common_mcp_fd"`
		CommonMCPSHA256      string   `json:"common_mcp_sha256"`
	}{
		Version:              executorVersion,
		Construction:         string(construction),
		LifecycleIdentity:    lifecycleIdentity,
		MaxStdoutBytes:       maxStdout,
		MaxStderrBytes:       maxStderr,
		MaxArtifactBytes:     maxArtifact,
		WaitDelayNanos:       waitDelay.Nanoseconds(),
		CleanupNanos:         cleanupTimeout.Nanoseconds(),
		Containment:          "nonpublishable-process-group",
		ArmInit:              "none",
		WritablePaths:        writablePaths,
		ReadOnlyPaths:        readOnlyPaths,
		ExecutablePaths:      executablePaths,
		FullFilesystemPolicy: fullFilesystemPolicy,
		NetworkPolicy:        networkPolicyIdentity(construction, containment != nil),
	}
	if containment != nil {
		canonical.Containment = containment.identity()
		canonical.ArmInit = struct { //nolint:govet,nolintlint // Field order defines the v3 arm-init hash input.
			Version            string `json:"version"`
			ExecutableSHA256   string `json:"executable_sha256"`
			LandlockABI        int    `json:"landlock_abi"`
			NoNewPrivileges    bool   `json:"no_new_privileges"`
			PreExecNonDumpable bool   `json:"pre_exec_non_dumpable"`
			TargetDumpable     bool   `json:"target_dumpable"`
			SeccompPolicy      string `json:"seccomp_policy"`
			PIDNamespace       bool   `json:"pid_namespace"`
			CapabilitiesEmpty  bool   `json:"capabilities_empty"`
		}{
			Version:            armInitVersion,
			ExecutableSHA256:   armInit.digest,
			SeccompPolicy:      armInitSeccompPolicy,
			LandlockABI:        landlockVersion,
			NoNewPrivileges:    true,
			PreExecNonDumpable: true,
			TargetDumpable:     true,
			PIDNamespace:       pidNamespace,
			CapabilitiesEmpty:  true,
		}
		canonical.CommonMCPFD = commonMCPExecutableFD
		if commonMCP != nil {
			canonical.CommonMCPSHA256 = commonMCP.digest
		}
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encode runner configuration: %w", err)
	}
	digest := sha256.Sum256(raw)
	return &Executor{
		active:               make(map[*preparedExecution]struct{}),
		lifecycle:            config.Lifecycle,
		lifecycleIdentity:    lifecycleIdentity,
		construction:         construction,
		maxStdout:            maxStdout,
		maxStderr:            maxStderr,
		maxArtifact:          maxArtifact,
		waitDelay:            waitDelay,
		cleanup:              cleanupTimeout,
		containment:          containment,
		armInit:              armInit,
		commonMCP:            commonMCP,
		commonSlot:           commonSlot,
		devNullRule:          devNullRule,
		landlockABI:          landlockVersion,
		writablePaths:        writablePaths,
		readOnlyPaths:        readOnlyPaths,
		executablePaths:      executablePaths,
		fullFilesystemPolicy: fullFilesystemPolicy,
		exactNetworkBoundary: exactNetworkBoundary,
		pidNamespace:         pidNamespace,
		identity: ExecutorIdentity{
			Kind:         "process",
			Version:      executorVersion,
			ConfigSHA256: hex.EncodeToString(digest[:]),
		},
	}, nil
}

func validateFilesystemPolicySeparation(
	writablePaths, readOnlyPaths, executablePaths []string,
) error {
	for _, writable := range writablePaths {
		for _, protected := range append(
			append(make([]string, 0, len(readOnlyPaths)+len(executablePaths)), readOnlyPaths...),
			executablePaths...,
		) {
			if filesystemPathsOverlap(writable, protected) {
				return fmt.Errorf(
					"landlock writable path %q overlaps protected read/execute path %q",
					writable,
					protected,
				)
			}
		}
	}
	return nil
}

func filesystemPathsOverlap(left, right string) bool {
	within := func(root, path string) bool {
		relative, err := filepath.Rel(root, path)
		return err == nil && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(left, right) || within(right, left)
}

// Identity returns the runner-owned, publication-neutral executor identity.
func (executor *Executor) Identity() ExecutorIdentity {
	return executor.identity
}

// Prepare returns before process launch so a publication layer can reverify
// pinned files after lifecycle initialization.
func (executor *Executor) Prepare(
	ctx context.Context,
	request ExecutionRequest,
) (PreparedExecution, error) {
	return executor.prepare(ctx, request, ordinaryExecutable)
}

// prepare accepts a private executable role so the privileged same-package
// test can exercise the version-scoped Go command-runner discovery pathname.
// Every production caller enters through Prepare and therefore uses the
// ordinary, script-rejecting role.
func (executor *Executor) prepare(
	ctx context.Context,
	request ExecutionRequest,
	role executableRole,
) (PreparedExecution, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.closed {
		return nil, NewIntegrityError(
			"prepare arm",
			errors.New("runner executor is closed"),
		)
	}
	request = cloneRequest(request)
	if err := harness.ValidateProcessSpec(request.Process); err != nil {
		return nil, fmt.Errorf("invalid approved process: %w", err)
	}
	if executor.containment != nil {
		for _, reserved := range []string{
			armInitMarkerEnvironment,
			armInitFDLayoutEnvironment,
			armInitProbeEnvironment,
			armInitPIDNamespaceEnvironment,
		} {
			if _, exists := request.Process.Environment[reserved]; exists {
				return nil, NewIntegrityError(
					"prepare arm-init environment",
					fmt.Errorf("approved process uses reserved environment key %s", reserved),
				)
			}
		}
	}
	prepared := &preparedExecution{executor: executor, request: request}
	executor.active[prepared] = struct{}{}
	executable, err := openPinnedExecutable(
		ctx,
		request,
		executor.construction == conformantCodexConstruction,
		role,
	)
	if err != nil {
		return prepared, NewIntegrityError("pin harness executable", err)
	}
	prepared.executable = executable
	if executor.containment != nil {
		arm, err := executor.containment.newArm()
		prepared.cgroup = arm
		if err != nil {
			return prepared, NewIntegrityError("create arm cgroup", err)
		}
	}
	if executor.lifecycle != nil {
		session, err := executor.lifecycle.BeginArm(ctx, cloneRequest(request))
		prepared.session = session
		if err != nil {
			return prepared, NewIntegrityError(
				"prepare arm lifecycle",
				err,
			)
		}
		if session == nil {
			return prepared, NewIntegrityError(
				"prepare arm lifecycle",
				errors.New("lifecycle returned no arm session"),
			)
		}
	}
	connectPorts, bindPorts, err := resolveArmNetworkPolicy(
		prepared.session,
		executor.construction == conformantCodexConstruction,
	)
	if err != nil {
		return prepared, NewIntegrityError("resolve arm network policy", err)
	}
	prepared.connectPorts = connectPorts
	prepared.bindPorts = bindPorts
	if executor.construction == conformantCodexConstruction {
		if prepared.cgroup == nil || len(connectPorts) != 1 {
			return prepared, NewIntegrityError(
				"install exact arm network policy",
				errors.New("conformant arm omitted its cgroup or sole proxy port"),
			)
		}
		if err := prepared.cgroup.installExactConnectPolicy(connectPorts[0]); err != nil {
			return prepared, NewIntegrityError("install exact arm network policy", err)
		}
	}
	if executor.containment != nil {
		if err := executor.armInit.reverify(); err != nil {
			return prepared, NewIntegrityError("reverify arm-init executable", err)
		}
		if executor.commonMCP != nil {
			if err := executor.commonMCP.reverify(); err != nil {
				return prepared, NewIntegrityError("reverify common scopesifter executable", err)
			}
		}
		roots, err := openWritableRoots(executor.writablePaths)
		prepared.writableRoots = roots
		if err != nil {
			return prepared, NewIntegrityError("pin Landlock writable roots", err)
		}
		roots, err = openPolicyRoots(executor.readOnlyPaths, false, false)
		prepared.readOnlyRoots = roots
		if err != nil {
			return prepared, NewIntegrityError("pin Landlock read-only roots", err)
		}
		roots, err = openPolicyRoots(
			executor.executablePaths,
			true,
			executor.construction == conformantCodexConstruction,
		)
		prepared.executableRoots = roots
		if err != nil {
			return prepared, NewIntegrityError("pin Landlock executable roots", err)
		}
	}
	return prepared, nil
}

// Close is the publication boundary for executor-owned activity. It prevents
// new arms, aborts every prepared arm, proves all contained process subtrees
// empty, and closes the delegated cgroup directory. A caller must require a
// nil result before loading signing authority.
func (executor *Executor) Close(ctx context.Context) error {
	if executor == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("close runner executor: context is required")
	}
	executor.closeMu.Lock()
	defer executor.closeMu.Unlock()
	executor.mu.Lock()
	if executor.fullyClosed {
		executor.mu.Unlock()
		return nil
	}
	executor.closed = true
	active := make([]*preparedExecution, 0, len(executor.active))
	for prepared := range executor.active {
		active = append(active, prepared)
	}
	executor.mu.Unlock()
	var resultErr error
	for _, prepared := range active {
		if err := prepared.Abort(ctx); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}
	executor.mu.Lock()
	remaining := len(executor.active)
	executor.mu.Unlock()
	if remaining != 0 {
		resultErr = errors.Join(
			resultErr,
			fmt.Errorf("runner executor still has %d active arm(s)", remaining),
		)
		return NewIntegrityError("close runner executor", resultErr)
	}
	executor.mu.Lock()
	containment := executor.containment
	executor.containment = nil
	executor.mu.Unlock()
	if containment != nil {
		if err := containment.close(); err != nil {
			executor.mu.Lock()
			executor.containment = containment
			executor.mu.Unlock()
			return NewIntegrityError("close runner executor containment", err)
		}
	}
	executor.mu.Lock()
	commonMCP := executor.commonMCP
	commonSlot := executor.commonSlot
	executor.commonMCP = nil
	executor.commonSlot = nil
	executor.mu.Unlock()
	if commonMCP != nil {
		if err := commonMCP.close(); err != nil {
			executor.mu.Lock()
			executor.commonMCP = commonMCP
			executor.commonSlot = commonSlot
			executor.mu.Unlock()
			return NewIntegrityError("close runner common FD5 executable", err)
		}
	} else if commonSlot != nil {
		if err := commonSlot.Close(); err != nil {
			executor.mu.Lock()
			executor.commonSlot = commonSlot
			executor.mu.Unlock()
			return NewIntegrityError("close runner common FD5 placeholder", err)
		}
	}
	executor.mu.Lock()
	devNullRule := executor.devNullRule
	executor.devNullRule = nil
	executor.mu.Unlock()
	if devNullRule != nil {
		if err := devNullRule.Close(); err != nil {
			executor.mu.Lock()
			executor.devNullRule = devNullRule
			executor.mu.Unlock()
			return NewIntegrityError("close runner /dev/null Landlock rule", err)
		}
	}
	executor.mu.Lock()
	armInit := executor.armInit
	executor.armInit = nil
	executor.mu.Unlock()
	if armInit != nil {
		if err := armInit.close(); err != nil {
			executor.mu.Lock()
			executor.armInit = armInit
			executor.mu.Unlock()
			return NewIntegrityError("close runner arm-init executable", err)
		}
	}
	executor.mu.Lock()
	executor.fullyClosed = true
	executor.mu.Unlock()
	return nil
}

func (executor *Executor) unregister(prepared *preparedExecution) {
	executor.mu.Lock()
	delete(executor.active, prepared)
	executor.mu.Unlock()
}

type preparedExecution struct {
	session         ArmSession
	executor        *Executor
	executable      *pinnedExecutionTarget
	cgroup          *armCgroup
	runCancel       context.CancelFunc
	runDone         chan struct{}
	bindPorts       []uint16
	connectPorts    []uint16
	executableRoots []*os.File
	readOnlyRoots   []*os.File
	writableRoots   []*os.File
	request         ExecutionRequest
	mu              sync.Mutex
	abortMu         sync.Mutex
	used            bool
	running         bool
	aborted         bool
}

func resolveArmNetworkPolicy(
	session ArmSession,
	requireConformant bool,
) ([]uint16, []uint16, error) {
	if session == nil {
		if requireConformant {
			return nil, nil, errors.New("conformant arm omitted its lifecycle session")
		}
		return []uint16{}, []uint16{}, nil
	}
	policy, ok := session.(ArmNetworkPolicy)
	if !ok {
		if requireConformant {
			return nil, nil, errors.New("conformant arm session omitted its network policy")
		}
		return []uint16{}, []uint16{}, nil
	}
	connect, err := validateNetworkPorts("connect", policy.AllowedConnectTCPPorts())
	if err != nil {
		return nil, nil, err
	}
	bind, err := validateNetworkPorts("bind", policy.AllowedBindTCPPorts())
	if err != nil {
		return nil, nil, err
	}
	if len(bind) != 0 {
		return nil, nil, errors.New("arm TCP bind policy must be empty")
	}
	if requireConformant && (len(connect) != 1 || len(bind) != 0) {
		return nil, nil, errors.New(
			"conformant arm network policy must allow one proxy connect port and no bind ports",
		)
	}
	return connect, bind, nil
}

func validateNetworkPorts(kind string, ports []uint16) ([]uint16, error) {
	if ports == nil || len(ports) > 16 {
		return nil, fmt.Errorf("arm %s TCP port policy is nil or oversized", kind)
	}
	result := append([]uint16(nil), ports...)
	for index, port := range result {
		if port == 0 || index != 0 && result[index-1] >= port {
			return nil, fmt.Errorf("arm %s TCP ports are not nonzero, sorted, and unique", kind)
		}
	}
	return result, nil
}

func (prepared *preparedExecution) Execute(
	ctx context.Context,
) (raw harness.RawExecution, resultErr error) {
	prepared.mu.Lock()
	if prepared.used || prepared.aborted {
		prepared.mu.Unlock()
		return harness.RawExecution{}, NewIntegrityError(
			"execute prepared arm",
			errors.New("prepared execution is already consumed"),
		)
	}
	prepared.used = true
	executionCtx, executionCancel := context.WithCancel(ctx)
	prepared.running = true
	prepared.runDone = make(chan struct{})
	prepared.runCancel = executionCancel
	prepared.mu.Unlock()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), prepared.executor.cleanup)
		defer cancel()
		if err := prepared.Abort(cleanupCtx); err != nil {
			resultErr = errors.Join(
				resultErr,
				NewIntegrityError("abort arm lifecycle", err),
			)
		}
	}()
	defer func() {
		prepared.mu.Lock()
		prepared.running = false
		prepared.runCancel = nil
		close(prepared.runDone)
		prepared.mu.Unlock()
		executionCancel()
	}()
	return prepared.run(executionCtx)
}

func (prepared *preparedExecution) run(
	ctx context.Context,
) (harness.RawExecution, error) {
	executor, request := prepared.executor, prepared.request
	if err := prepared.executable.reverify(); err != nil {
		return harness.RawExecution{}, NewIntegrityError(
			"reverify immutable harness executable before launch",
			err,
		)
	}
	processContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(request.Process.TimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	stdout := newLimitBuffer(executor.maxStdout, cancel)
	stderr := newLimitBuffer(executor.maxStderr, cancel)
	command := exec.CommandContext(processContext, "/proc/self/fd/3", request.Process.Argv[1:]...)
	command.Args[0] = request.Process.Argv[0]
	command.Dir = request.Process.Directory
	command.Env = environmentList(request.Process.Environment)
	if prepared.cgroup == nil {
		command.ExtraFiles = []*os.File{prepared.executable.file}
	} else {
		command.ExtraFiles = make(
			[]*os.File,
			0,
			4+len(prepared.writableRoots)+len(prepared.readOnlyRoots)+len(prepared.executableRoots),
		)
		command.ExtraFiles = append(
			command.ExtraFiles,
			executor.armInit.launchFile,
			prepared.executable.file,
			executor.commonSlot,
			executor.devNullRule,
		)
		command.ExtraFiles = append(command.ExtraFiles, prepared.writableRoots...)
		command.ExtraFiles = append(command.ExtraFiles, prepared.readOnlyRoots...)
		command.ExtraFiles = append(command.ExtraFiles, prepared.executableRoots...)
		command.Env = append(
			command.Env,
			armInitMarkerEnvironment+"="+armInitVersion,
			armInitFDLayoutEnvironment+"="+formatArmInitFDLayout(
				len(prepared.writableRoots),
				len(prepared.readOnlyRoots),
				len(prepared.executableRoots),
				prepared.connectPorts,
				prepared.bindPorts,
			),
		)
		if executor.pidNamespace {
			command.Env = append(
				command.Env,
				armInitPIDNamespaceEnvironment+"="+armInitVersion,
			)
		}
	}
	command.Stdin = bytes.NewReader(request.Process.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = executor.waitDelay
	if prepared.cgroup == nil {
		isolateCommand(command)
	} else {
		if executor.exactNetworkBoundary {
			if err := prepared.cgroup.verifyExactConnectPolicy(); err != nil {
				return harness.RawExecution{}, NewIntegrityError(
					"verify exact arm network policy before launch",
					err,
				)
			}
		}
		if err := configureContainedCommand(
			command,
			prepared.cgroup,
			executor.pidNamespace,
		); err != nil {
			return harness.RawExecution{}, NewIntegrityError("configure arm cgroup", err)
		}
	}
	runErr := command.Run()
	var resources *harness.ResourceOutcome
	if prepared.cgroup == nil {
		cleanupCommandGroup(command)
	} else if err := prepared.cgroup.killAndRemove(executor.cleanup); err != nil {
		return harness.RawExecution{}, NewIntegrityError("prove arm cgroup cleanup", err)
	} else {
		resources = prepared.cgroup.resourceOutcome()
		if resources == nil {
			return harness.RawExecution{}, NewIntegrityError(
				"capture arm cgroup resources",
				errors.New("contained execution omitted its resource outcome"),
			)
		}
	}
	if prepared.cgroup != nil {
		if err := executor.armInit.reverify(); err != nil {
			return harness.RawExecution{}, NewIntegrityError("reverify arm-init after execution", err)
		}
		if executor.commonMCP != nil {
			if err := executor.commonMCP.reverify(); err != nil {
				return harness.RawExecution{}, NewIntegrityError(
					"reverify common scopesifter after execution",
					err,
				)
			}
		}
	}
	if err := prepared.executable.reverify(); err != nil {
		return harness.RawExecution{}, NewIntegrityError(
			"reverify immutable harness executable after execution",
			err,
		)
	}
	raw := harness.RawExecution{
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		Artifacts:       []harness.Artifact{},
		Resources:       resources,
		ExitCode:        exitCode(command, runErr),
		LaunchFailed:    runErr != nil && command.ProcessState == nil && processContext.Err() == nil,
		TimedOut:        errors.Is(processContext.Err(), context.DeadlineExceeded) && ctx.Err() == nil,
		Cancelled:       ctx.Err() != nil,
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if err := validateRawCapture(raw); err != nil {
		return raw, NewIntegrityError("validate raw process capture", err)
	}
	if prepared.session != nil {
		finishCtx, finishCancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			executor.cleanup,
		)
		artifacts, err := prepared.session.Finish(
			finishCtx,
			cloneRequest(request),
			cloneRawExecution(raw),
		)
		finishCancel()
		artifactErr := validateArmArtifacts(artifacts, executor.maxArtifact)
		if artifactErr == nil {
			raw.Artifacts = cloneArtifacts(artifacts)
			if captureErr := validateRawCapture(raw); captureErr != nil {
				artifactErr = NewIntegrityError("validate completed raw capture", captureErr)
			}
		} else {
			artifactErr = NewIntegrityError("validate arm artifacts", artifactErr)
		}
		var finishErr error
		if err != nil {
			finishErr = NewIntegrityError("finish arm lifecycle", err)
		}
		if resultErr := errors.Join(artifactErr, finishErr); resultErr != nil {
			return raw, resultErr
		}
	}
	if runErr == nil {
		return raw, nil
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) || processContext.Err() != nil ||
		stdout.Truncated() || stderr.Truncated() {
		return raw, nil
	}
	return raw, fmt.Errorf("start or wait for process: %w", runErr)
}

func validateArmArtifacts(artifacts []harness.Artifact, maximumBytes int) error {
	if err := harness.ValidateArtifacts(artifacts); err != nil {
		return err
	}
	total := 0
	for _, artifact := range artifacts {
		if len(artifact.Data) > maximumBytes-total {
			return errors.New("runner artifacts exceeded configured byte limit")
		}
		total += len(artifact.Data)
	}
	return nil
}

func validateRawCapture(raw harness.RawExecution) error {
	if len(raw.Stdout) > harness.MaxRawStreamBytes ||
		len(raw.Stderr) > harness.MaxRawStreamBytes {
		return errors.New("raw process stream exceeds its schema byte limit")
	}
	if err := harness.ValidateArtifacts(raw.Artifacts); err != nil {
		return fmt.Errorf("raw process artifacts: %w", err)
	}
	if raw.Resources != nil {
		if err := harness.ValidateResourceOutcome(raw.Resources); err != nil {
			return fmt.Errorf("raw process resources: %w", err)
		}
	}
	if raw.LaunchFailed && (raw.ExitCode != -1 || raw.TimedOut || raw.Cancelled ||
		raw.StdoutTruncated || raw.StderrTruncated) {
		return errors.New("launch failure conflicts with process termination state")
	}
	if raw.TimedOut && (raw.Cancelled || raw.StdoutTruncated || raw.StderrTruncated) {
		return errors.New("timeout conflicts with cancellation or stream truncation")
	}
	if raw.Cancelled && (raw.StdoutTruncated || raw.StderrTruncated) {
		return errors.New("cancellation conflicts with stream truncation")
	}
	return nil
}

// Abort implements PreparedExecution.
func (prepared *preparedExecution) Abort(ctx context.Context) error {
	if ctx == nil {
		return errors.New("abort prepared execution: context is required")
	}
	prepared.abortMu.Lock()
	defer prepared.abortMu.Unlock()
	prepared.mu.Lock()
	if prepared.aborted {
		prepared.mu.Unlock()
		return nil
	}
	session := prepared.session
	executable := prepared.executable
	cgroup := prepared.cgroup
	writableRoots := prepared.writableRoots
	readOnlyRoots := prepared.readOnlyRoots
	executableRoots := prepared.executableRoots
	running := prepared.running
	runDone := prepared.runDone
	runCancel := prepared.runCancel
	prepared.mu.Unlock()
	if running {
		runCancel()
		select {
		case <-runDone:
		case <-ctx.Done():
			return fmt.Errorf("wait for running arm before abort: %w", ctx.Err())
		}
	}
	var containmentErr error
	if cgroup != nil {
		containmentErr = cgroup.killAndRemove(prepared.executor.cleanup)
	}
	var closeErr error
	if executable != nil {
		closeErr = executable.close()
		if closeErr == nil {
			prepared.mu.Lock()
			prepared.executable = nil
			prepared.mu.Unlock()
		}
	}
	for _, root := range writableRoots {
		closeErr = errors.Join(closeErr, root.Close())
	}
	for _, root := range readOnlyRoots {
		closeErr = errors.Join(closeErr, root.Close())
	}
	for _, root := range executableRoots {
		closeErr = errors.Join(closeErr, root.Close())
	}
	prepared.mu.Lock()
	prepared.writableRoots = nil
	prepared.readOnlyRoots = nil
	prepared.executableRoots = nil
	prepared.mu.Unlock()
	if session == nil {
		err := errors.Join(containmentErr, closeErr)
		if err != nil {
			return err
		}
		prepared.mu.Lock()
		prepared.aborted = true
		prepared.mu.Unlock()
		prepared.executor.unregister(prepared)
		return nil
	}
	err := errors.Join(containmentErr, session.Abort(ctx), closeErr)
	if err == nil {
		prepared.mu.Lock()
		prepared.aborted = true
		prepared.mu.Unlock()
		prepared.executor.unregister(prepared)
	}
	return err
}

type pinnedExecutionTarget struct {
	info          os.FileInfo
	file          *os.File
	path          string
	digest        string
	requireVerity bool
}

func (target *pinnedExecutionTarget) close() error {
	if target == nil || target.file == nil {
		return nil
	}
	err := target.file.Close()
	if err == nil {
		target.file = nil
	}
	return err
}

func (target *pinnedExecutionTarget) reverify() error {
	if target == nil || target.file == nil {
		return errors.New("pinned execution target is closed")
	}
	opened, err := target.file.Stat()
	if err != nil || !os.SameFile(opened, target.info) || opened.Mode() != target.info.Mode() ||
		opened.Size() != target.info.Size() || !opened.ModTime().Equal(target.info.ModTime()) ||
		executableLinkCount(opened) != executableLinkCount(target.info) {
		return errors.New("pinned execution target metadata changed")
	}
	pathInfo, err := os.Lstat(target.path)
	if err != nil || !os.SameFile(opened, pathInfo) {
		return errors.New("pinned execution target pathname changed")
	}
	if target.requireVerity {
		if err := requireFSVerity(target.file); err != nil {
			return err
		}
	}
	return nil
}

func openPinnedExecutable(
	ctx context.Context,
	request ExecutionRequest,
	requireStatic bool,
	role executableRole,
) (*pinnedExecutionTarget, error) {
	path := request.Process.Argv[0]
	if path != request.Invocation.Executable {
		return nil, errors.New("approved process executable does not match invocation")
	}
	switch role {
	case ordinaryExecutable:
		if err := processpolicy.ValidateExecutable(path); err != nil {
			return nil, fmt.Errorf("approved executable violates process policy: %w", err)
		}
	case verifiedCommandRunnerDiscovery:
		if !commandrunner.Invoked(path, request.Process.Environment["PATH"]) {
			return nil, errors.New("verified command-runner discovery path has an invalid argv0 or PATH shape")
		}
	default:
		return nil, errors.New("approved executable has an unknown process role")
	}
	if !validSHA256(request.Invocation.ExecutableSHA256) {
		return nil, errors.New("invocation executable digest is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 ||
		before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("approved executable is not an executable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("approved executable changed while opening")
	}
	if err := processpolicy.ValidateNativeFile(file); err != nil {
		return nil, err
	}
	// Conformant targets are executed from the exact fs-verity-protected
	// execution-snapshot pathname committed by the plan. Unlike a deleted
	// memfd, this preserves Codex's required current_exe self-dispatch paths.
	if requireStatic {
		if err := requireFSVerity(file); err != nil {
			return nil, err
		}
	}
	if requireStatic || role == verifiedCommandRunnerDiscovery {
		if err := validateStaticELF(file); err != nil {
			return nil, err
		}
	}
	digest, err := hashOpenFile(file)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("approved executable changed while pinning")
	}
	if digest != request.Invocation.ExecutableSHA256 {
		return nil, errors.New("approved executable digest changed before launch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if role == verifiedCommandRunnerDiscovery {
		if err := commandrunner.VerifyPinnedEntrypoint(ctx, path, file); err != nil {
			return nil, fmt.Errorf("prove verified command-runner discovery image: %w", err)
		}
	}
	valid = true
	return &pinnedExecutionTarget{
		file: file, path: path, digest: digest, info: opened, requireVerity: requireStatic,
	}, nil
}

type executableRole uint8

const (
	ordinaryExecutable executableRole = iota
	verifiedCommandRunnerDiscovery
)

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		value == hex.EncodeToString(decoded)
}

func exitCode(command *exec.Cmd, runErr error) int {
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode()
	}
	if runErr == nil {
		return 0
	}
	return -1
}

type limitBuffer struct {
	cancel    context.CancelFunc
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newLimitBuffer(limit int, cancel context.CancelFunc) *limitBuffer {
	return &limitBuffer{limit: limit, cancel: cancel}
}

func (buffer *limitBuffer) Write(content []byte) (int, error) {
	written := len(content)
	if buffer.truncated {
		return written, nil
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining >= len(content) {
		_, _ = buffer.buffer.Write(content)
		return written, nil
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(content[:remaining])
	}
	buffer.truncated = true
	buffer.cancel()
	return written, nil
}

func (buffer *limitBuffer) Bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func (buffer *limitBuffer) Truncated() bool {
	return buffer.truncated
}

func environmentList(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func cloneArtifacts(source []harness.Artifact) []harness.Artifact {
	clone := make([]harness.Artifact, len(source))
	for index, artifact := range source {
		clone[index] = artifact
		clone[index].Data = append([]byte(nil), artifact.Data...)
	}
	return clone
}

func cloneRequest(source ExecutionRequest) ExecutionRequest {
	clone := source
	clone.Process = cloneProcess(source.Process)
	clone.Invocation = cloneInvocation(source.Invocation)
	return clone
}

func cloneProcess(source harness.ProcessSpec) harness.ProcessSpec {
	clone := source
	clone.Environment = cloneMap(source.Environment)
	clone.Argv = append([]string(nil), source.Argv...)
	clone.Stdin = append([]byte(nil), source.Stdin...)
	return clone
}

func cloneInvocation(source harness.Invocation) harness.Invocation {
	clone := source
	clone.Environment = cloneMap(source.Environment)
	clone.Arguments = append([]string(nil), source.Arguments...)
	clone.Prompt = append([]byte(nil), source.Prompt...)
	clone.MCPServers = make([]harness.MCPServer, len(source.MCPServers))
	for index, server := range source.MCPServers {
		clone.MCPServers[index] = server
		clone.MCPServers[index].Environment = cloneMap(server.Environment)
		clone.MCPServers[index].Arguments = append([]string(nil), server.Arguments...)
	}
	return clone
}

func cloneRawExecution(source harness.RawExecution) harness.RawExecution {
	clone := source
	clone.Stdout = append([]byte(nil), source.Stdout...)
	clone.Stderr = append([]byte(nil), source.Stderr...)
	clone.Artifacts = cloneArtifacts(source.Artifacts)
	clone.Resources = harness.CloneResourceOutcome(source.Resources)
	return clone
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
