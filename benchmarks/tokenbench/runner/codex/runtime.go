// Package codex implements the Codex-specific arm lifecycle and local
// Responses capture proxy used by tokenbench's generic process runner.
package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
	harnesscodex "github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness/codex"
	genericrunner "github.com/scopesifter/scopesifter/benchmarks/tokenbench/runner"
)

const (
	lifecycleVersion = "tokenbench.codex-runner/codex-cli-v0.144.0/v3"
	stateMarkerName  = ".tokenbench-codex-runtime-v3"
	stateMarkerData  = lifecycleVersion + "\n"

	productionUpstreamOrigin = "https://api.openai.com"
	productionUpstreamDNS    = "api.openai.com"
	localCapabilityPrefix    = "tokenbench-local-proxy/v1/"
	// ProductionRoute is the exact nonsecret provider-route policy accepted by
	// the conformant runner. The concrete endpoint is /v1/responses.
	ProductionRoute = "openai-api/v1:https://api.openai.com/v1"
	// ProductionNetworkPolicy commits the network/TLS verification mechanism
	// used by production publication. Go's system root store is the only CA
	// source and the HTTP transport has no proxy callback. This identity does
	// not pretend CertPool subjects identify exact trust anchors: each actual
	// verified DER chain and DNS name is committed in the Responses trace.
	ProductionNetworkPolicy = "go-system-roots/v1;proxy=disabled;ambient-overrides=forbidden"

	defaultMaxRequestBytes  = 16 << 20
	defaultMaxResponseBytes = 32 << 20
	defaultMaxSSEEventBytes = 1 << 20
	defaultMaxEvents        = 100000
	defaultMaxRequests      = 4096
	defaultMaxHeaderBytes   = 64 << 10
	defaultHeaderTimeout    = 5 * time.Second
	defaultUpstreamTimeout  = 30 * time.Second
)

var productionNetworkOverrideEnvironment = []string{
	"ALL_PROXY",
	"CODEX_CA_CERTIFICATE",
	"CURL_CA_BUNDLE",
	"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH",
	"HTTPS_PROXY",
	"HTTP_PROXY",
	"NODE_EXTRA_CA_CERTS",
	"NO_PROXY",
	"OPENAI_API_BASE",
	"OPENAI_BASE_URL",
	"REQUESTS_CA_BUNDLE",
	"SSL_CERT_DIR",
	"SSL_CERT_FILE",
	"all_proxy",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

// Config controls the fixed local proxy, private runtime tree, immutable
// toolbox PATH, resource bounds, and upstream route. UpstreamCredential is
// deliberately excluded from Identity and every serialized structure.
type Config struct {
	StateRoot          string
	ToolboxRoot        string
	ListenAddress      string
	UpstreamURL        string
	UpstreamCredential string
	MaxRequestBytes    int64
	MaxResponseBytes   int64
	MaxSSEEventBytes   int
	MaxEvents          int
	MaxRequests        int
	MaxHeaderBytes     int
	ReadHeaderTimeout  time.Duration
	UpstreamTimeout    time.Duration
}

// ProductionConfig controls the sole lifecycle construction eligible for
// conformant publication. ToolboxRoot must name the immutable execution
// snapshot's closed toolbox directory. Unlike Config, it intentionally exposes
// no upstream route: production traffic is pinned to OpenAI's canonical /v1
// API.
type ProductionConfig struct {
	StateRoot          string
	ToolboxRoot        string
	ListenAddress      string
	UpstreamCredential string
	MaxRequestBytes    int64
	MaxResponseBytes   int64
	MaxSSEEventBytes   int
	MaxEvents          int
	MaxRequests        int
	MaxHeaderBytes     int
	ReadHeaderTimeout  time.Duration
	UpstreamTimeout    time.Duration
}

// Lifecycle owns one reserved loopback listener and one private runtime tree.
// It supports a single active arm while retaining only sanitized first-arm
// state until the matching second arm is compared.
type Lifecycle struct {
	listener          net.Listener
	serveErr          error
	pairs             map[int]map[genericrunner.Arm]armSnapshot
	closePermit       chan struct{}
	serveDone         chan struct{}
	client            *http.Client
	active            *ArmSession
	finalizing        *ArmSession
	stateRoot         *os.Root
	server            *http.Server
	upstreamURL       *url.URL
	layout            RuntimeLayout
	credential        string
	identity          string
	productionNetwork string
	limits            limits
	mu                sync.Mutex
	proxyPort         uint16
	closed            bool
	fullyClosed       bool
	production        bool
}

type productionSecurity struct {
	rootCAs         *x509.CertPool
	networkIdentity string
}

type limits struct {
	maxRequestBytes  int64
	maxResponseBytes int64
	maxSSEEventBytes int
	maxEvents        int
	maxRequests      int
	maxHeaderBytes   int
	headerTimeout    time.Duration
	upstreamTimeout  time.Duration
}

type armSnapshot struct {
	captureError string
	config       []byte
	configCommon []byte
	configDelta  []byte
	trace        harnesscodex.ResponsesTrace
	requestCount int
	ordinary     bool
}

// ArmSession implements runner.ArmSession. It is bound to exactly one cloned
// ExecutionRequest and is finalized by either Finish or Abort.
type ArmSession struct {
	fatal            error
	responseAttempts map[int]harnesscodex.ProviderResponseAttemptTrace
	lifecycle        *Lifecycle
	responses        map[int]harnesscodex.ResponsesResponseTrace
	cleanupPermit    chan struct{}
	finishReturned   chan struct{}
	inflightDrained  chan struct{}
	turnState        string
	expectedProvider string
	requestSHA256    string
	expectedModel    string
	turnStateSHA256  string
	declarations     []harnesscodex.ResponsesToolDeclaration
	requests         []harnesscodex.ResponsesRequestSnapshot
	request          genericrunner.ExecutionRequest
	trace            harnesscodex.ResponsesTrace
	inflight         sync.WaitGroup
	requestCount     int
	drainOnce        sync.Once
	mu               sync.Mutex
	state            sessionState
}

type sessionState uint8

const (
	sessionActive sessionState = iota
	sessionFinishing
	sessionAborting
	sessionFinished
	sessionAborted
)

var _ genericrunner.Lifecycle = (*Lifecycle)(nil)

// New reserves the loopback listener immediately, claims an empty private
// state root, starts the proxy, and returns a lifecycle whose URL cannot change
// between arm preparations.
func New(config Config) (*Lifecycle, error) {
	return newLifecycle(config, nil)
}

// NewProduction constructs the only lifecycle eligible for conformant
// publication. The upstream route cannot be supplied or redirected by a
// caller; tests and private mirrors must use New and remain nonpublishable.
func NewProduction(config ProductionConfig) (*Lifecycle, error) {
	if err := ValidateProductionEnvironment(); err != nil {
		return nil, err
	}
	if config.UpstreamTimeout <= 0 {
		return nil, errors.New(
			"production Codex upstream timeout must be explicit and match the suite timeout",
		)
	}
	security, err := snapshotProductionSecurity()
	if err != nil {
		return nil, err
	}
	return newLifecycle(Config{
		StateRoot:          config.StateRoot,
		ToolboxRoot:        config.ToolboxRoot,
		ListenAddress:      config.ListenAddress,
		UpstreamURL:        productionUpstreamOrigin,
		UpstreamCredential: config.UpstreamCredential,
		MaxRequestBytes:    config.MaxRequestBytes,
		MaxResponseBytes:   config.MaxResponseBytes,
		MaxSSEEventBytes:   config.MaxSSEEventBytes,
		MaxEvents:          config.MaxEvents,
		MaxRequests:        config.MaxRequests,
		MaxHeaderBytes:     config.MaxHeaderBytes,
		ReadHeaderTimeout:  config.ReadHeaderTimeout,
		UpstreamTimeout:    config.UpstreamTimeout,
	}, security)
}

// ValidateProductionEnvironment fails closed if ambient proxy, provider-base,
// or custom-CA variables could alter the production route or trust roots. The
// CLI calls this before reading its credential; NewProduction repeats it at
// the construction boundary.
func ValidateProductionEnvironment() error {
	for _, name := range productionNetworkOverrideEnvironment {
		if _, exists := os.LookupEnv(name); exists {
			return fmt.Errorf(
				"production Codex network policy forbids ambient variable %s",
				name,
			)
		}
	}
	return nil
}

func snapshotProductionSecurity() (*productionSecurity, error) {
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("load production Codex system TLS roots")
	}
	canonical := struct { //nolint:govet,nolintlint // Field order is the production network identity contract.
		Schema                string   `json:"schema"`
		Route                 string   `json:"route"`
		TLSMinimumVersion     uint16   `json:"tls_minimum_version"`
		Proxy                 string   `json:"proxy"`
		ForbiddenEnvironment  []string `json:"forbidden_environment"`
		TLSConnectionEvidence string   `json:"tls_connection_evidence"`
	}{
		Schema:                ProductionNetworkPolicy,
		Route:                 ProductionRoute,
		TLSMinimumVersion:     tls.VersionTLS12,
		Proxy:                 "disabled",
		ForbiddenEnvironment:  append([]string(nil), productionNetworkOverrideEnvironment...),
		TLSConnectionEvidence: "ordered-sha256-der-chains+verified-dns-name/v1",
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, errors.New("encode production Codex TLS root policy")
	}
	digest := sha256.Sum256(raw)
	return &productionSecurity{
		rootCAs: rootCAs,
		networkIdentity: ProductionNetworkPolicy + "/sha256:" +
			hex.EncodeToString(digest[:]),
	}, nil
}

func newLifecycle(config Config, production *productionSecurity) (*Lifecycle, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", resolved.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("reserve Codex proxy listener: %w", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		return nil, joinCloseError(
			errors.New("codex proxy listener did not resolve to IPv4 loopback"),
			listener,
			"close Codex proxy listener",
		)
	}
	localCapability, err := newLocalProxyCapability()
	if err != nil {
		return nil, joinCloseError(err, listener, "close Codex proxy listener")
	}
	if localCapability == config.UpstreamCredential {
		return nil, joinCloseError(
			errors.New("local proxy capability must differ from the upstream credential"),
			listener,
			"close Codex proxy listener",
		)
	}
	layout := RuntimeLayout{
		ProxyURL:             fmt.Sprintf("http://127.0.0.1:%d/v1", address.Port),
		Home:                 filepath.Join(resolved.stateRoot, "home"),
		CodexHome:            filepath.Join(resolved.stateRoot, "codex-home"),
		Temp:                 filepath.Join(resolved.stateRoot, "tmp"),
		ConfigLock:           filepath.Join(resolved.stateRoot, "config-lock"),
		ToolboxRoot:          resolved.toolboxRoot,
		LocalProxyCapability: localCapability,
	}
	if err := layout.Validate(); err != nil {
		return nil, joinCloseError(err, listener, "close Codex proxy listener")
	}
	root, err := claimStateRoot(resolved.stateRoot)
	if err != nil {
		return nil, joinCloseError(err, listener, "close Codex proxy listener")
	}

	client := newUpstreamClient(resolved.limits.upstreamTimeout)
	productionNetwork := ""
	if production != nil {
		client = newProductionUpstreamClient(
			resolved.limits.upstreamTimeout,
			production.rootCAs,
		)
		productionNetwork = production.networkIdentity
	}
	closePermit := make(chan struct{}, 1)
	closePermit <- struct{}{}
	serveDone := make(chan struct{})
	lifecycle := &Lifecycle{
		layout:            layout,
		stateRoot:         root,
		listener:          listener,
		proxyPort:         uint16(address.Port),
		upstreamURL:       resolved.upstreamURL,
		credential:        config.UpstreamCredential,
		client:            client,
		closePermit:       closePermit,
		serveDone:         serveDone,
		limits:            resolved.limits,
		pairs:             make(map[int]map[genericrunner.Arm]armSnapshot),
		production:        production != nil,
		productionNetwork: productionNetwork,
	}
	route := ""
	if production != nil {
		route = ProductionRoute
	}
	identity, err := lifecycleIdentity(
		layout,
		resolved.upstreamURL,
		resolved.limits,
		route,
		productionNetwork,
	)
	if err != nil {
		err = joinCloseError(err, root, "close Codex StateRoot")
		err = joinCloseError(err, listener, "close Codex proxy listener")
		return nil, err
	}
	lifecycle.identity = identity
	if err := lifecycle.resetLayout(context.Background()); err != nil {
		err = joinCloseError(err, root, "close Codex StateRoot")
		err = joinCloseError(err, listener, "close Codex proxy listener")
		return nil, err
	}
	lifecycle.server = &http.Server{
		Handler:           lifecycle,
		ReadHeaderTimeout: resolved.limits.headerTimeout,
		MaxHeaderBytes:    resolved.limits.maxHeaderBytes,
	}
	go func() {
		defer close(lifecycle.serveDone)
		err := lifecycle.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			lifecycle.mu.Lock()
			lifecycle.serveErr = fmt.Errorf("codex proxy server stopped: %w", err)
			lifecycle.mu.Unlock()
		}
	}()
	return lifecycle, nil
}

func joinCloseError(cause error, closer io.Closer, operation string) error {
	if err := closer.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("%s: %w", operation, err))
	}
	return cause
}

// RuntimeLayout returns the exact publishable layout the adapter must commit.
func (lifecycle *Lifecycle) RuntimeLayout() RuntimeLayout {
	return lifecycle.layout
}

// FilesystemWritePaths returns the complete code-owned writable allowlist for
// the Codex child. The conformant runner binds these exact four directories
// into containment identity; callers cannot add another writable path.
func (lifecycle *Lifecycle) FilesystemWritePaths() []string {
	if lifecycle == nil {
		return nil
	}
	paths := []string{
		lifecycle.layout.Home,
		lifecycle.layout.CodexHome,
		lifecycle.layout.Temp,
		lifecycle.layout.ConfigLock,
	}
	sort.Strings(paths)
	return paths
}

// Identity implements runner.Lifecycle. It is deterministic from public
// configuration and intentionally independent of UpstreamCredential.
func (lifecycle *Lifecycle) Identity() string {
	if lifecycle == nil {
		return ""
	}
	return lifecycle.identity
}

// ProductionRouteIdentity reports the immutable nonsecret provider route
// authority carried by this lifecycle. Generic New constructions always
// return false, even when configured with the same URL text.
func (lifecycle *Lifecycle) ProductionRouteIdentity() (string, bool) {
	if lifecycle == nil || !lifecycle.production {
		return "", false
	}
	return ProductionRoute, true
}

// ProductionNetworkPolicyIdentity reports the immutable verification/no-proxy
// mechanism carried by this lifecycle. Exact provider certificates are
// dynamic connection evidence, not inferred from root-subject names here.
func (lifecycle *Lifecycle) ProductionNetworkPolicyIdentity() (string, bool) {
	if lifecycle == nil || !lifecycle.production ||
		!ValidProductionNetworkPolicyIdentity(lifecycle.productionNetwork) {
		return "", false
	}
	return lifecycle.productionNetwork, true
}

// ValidProductionNetworkPolicyIdentity validates the public commitment form
// without consulting ambient trust roots.
func ValidProductionNetworkPolicyIdentity(value string) bool {
	prefix := ProductionNetworkPolicy + "/sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == digest
}

// BeginArm implements runner.Lifecycle. It validates that the approved process
// already contains the exact shared layout, resets private state, and opens one
// capture session without mutating the launch request.
func (lifecycle *Lifecycle) BeginArm(
	ctx context.Context,
	request genericrunner.ExecutionRequest,
) (genericrunner.ArmSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request = cloneExecutionRequest(request)
	if err := lifecycle.validateRequest(request); err != nil {
		return nil, err
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return nil, err
	}
	expectedProvider, err := providerModel(request.Invocation.ModelRevision)
	if err != nil {
		return nil, err
	}

	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.closed {
		return nil, errors.New("codex lifecycle is closed")
	}
	if lifecycle.serveErr != nil {
		return nil, lifecycle.serveErr
	}
	if lifecycle.active != nil || lifecycle.finalizing != nil {
		return nil, errors.New("codex lifecycle already has an active or finalizing arm")
	}
	if prior := lifecycle.pairs[request.Repetition]; prior != nil {
		if _, duplicate := prior[request.Arm]; duplicate {
			return nil, errors.New("codex arm was already captured for this repetition")
		}
	}
	if err := lifecycle.resetLayout(ctx); err != nil {
		return nil, err
	}
	cleanupPermit := make(chan struct{}, 1)
	cleanupPermit <- struct{}{}
	session := &ArmSession{
		lifecycle:        lifecycle,
		request:          request,
		requestSHA256:    requestDigest,
		expectedModel:    request.Invocation.Model,
		expectedProvider: expectedProvider,
		trace: harnesscodex.ResponsesTrace{
			SchemaVersion: harnesscodex.ResponsesTraceSchemaVersion,
			Requests:      make([]harnesscodex.ResponsesRequestSnapshot, 0),
			Responses:     make([]harnesscodex.ResponsesResponseTrace, 0),
		},
		requests:         make([]harnesscodex.ResponsesRequestSnapshot, 0),
		responseAttempts: make(map[int]harnesscodex.ProviderResponseAttemptTrace),
		responses:        make(map[int]harnesscodex.ResponsesResponseTrace),
		cleanupPermit:    cleanupPermit,
		finishReturned:   make(chan struct{}),
		inflightDrained:  make(chan struct{}),
		state:            sessionActive,
	}
	lifecycle.active = session
	return session, nil
}

// Close shuts down the reserved proxy and clears runner-owned state. It never
// reads ambient Codex configuration or any credential source.
func (lifecycle *Lifecycle) Close(ctx context.Context) error {
	if lifecycle == nil {
		return errors.New("codex lifecycle is unavailable")
	}
	lifecycle.mu.Lock()
	fullyClosed := lifecycle.fullyClosed
	lifecycle.mu.Unlock()
	if fullyClosed {
		return nil
	}
	if ctx == nil {
		return errors.New("codex lifecycle close context is nil")
	}
	select {
	case <-lifecycle.closePermit:
	default:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-lifecycle.closePermit:
		}
	}
	defer func() { lifecycle.closePermit <- struct{}{} }()

	lifecycle.mu.Lock()
	if lifecycle.fullyClosed {
		lifecycle.mu.Unlock()
		return nil
	}
	lifecycle.closed = true
	active := lifecycle.active
	lifecycle.mu.Unlock()

	var closeErrors []error
	if active != nil {
		if _, err := active.startAbort(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if err := lifecycle.drainFinalization(ctx); err != nil {
		closeErrors = append(closeErrors, err)
	}
	if lifecycle.server != nil {
		if err := lifecycle.server.Shutdown(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("shut down Codex proxy: %w", err))
		}
		select {
		case <-lifecycle.serveDone:
		default:
			select {
			case <-ctx.Done():
				closeErrors = append(closeErrors, ctx.Err())
			case <-lifecycle.serveDone:
			}
		}
	}
	lifecycle.mu.Lock()
	if lifecycle.active != nil || lifecycle.finalizing != nil {
		closeErrors = append(closeErrors, errors.New(
			"codex lifecycle retained an active or finalizing arm after shutdown",
		))
	}
	lifecycle.mu.Unlock()
	if len(closeErrors) != 0 {
		return errors.Join(closeErrors...)
	}

	// Finalization can add or reconcile the last pair snapshot. Read this only
	// after the registered finalizer has drained and returned.
	lifecycle.mu.Lock()
	unmatchedPairs := len(lifecycle.pairs)
	client := lifecycle.client
	stateRoot := lifecycle.stateRoot
	serveErr := lifecycle.serveErr
	lifecycle.mu.Unlock()
	if unmatchedPairs != 0 {
		closeErrors = append(closeErrors, unmatchedPairError(unmatchedPairs))
	}
	if serveErr != nil {
		closeErrors = append(closeErrors, serveErr)
	}
	if err := lifecycle.resetLayout(ctx); err != nil {
		return errors.Join(append(closeErrors, err)...)
	}
	if stateRoot != nil {
		if err := stateRoot.Close(); err != nil {
			return errors.Join(append(
				closeErrors,
				fmt.Errorf("close Codex state root: %w", err),
			)...)
		}
		lifecycle.mu.Lock()
		if lifecycle.stateRoot == stateRoot {
			lifecycle.stateRoot = nil
		}
		lifecycle.mu.Unlock()
	}
	if client != nil {
		client.CloseIdleConnections()
	}
	result := errors.Join(closeErrors...)
	lifecycle.mu.Lock()
	lifecycle.credential = ""
	lifecycle.client = nil
	lifecycle.fullyClosed = result == nil
	lifecycle.mu.Unlock()
	return result
}

func (lifecycle *Lifecycle) drainFinalization(ctx context.Context) error {
	for {
		lifecycle.mu.Lock()
		session := lifecycle.finalizing
		lifecycle.mu.Unlock()
		if session == nil {
			return nil
		}

		session.mu.Lock()
		state := session.state
		finishReturned := session.finishReturned
		session.mu.Unlock()
		switch state {
		case sessionFinishing:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-finishReturned:
			}
		case sessionAborting:
			if err := session.completeAbort(ctx); err != nil {
				return err
			}
		case sessionFinished, sessionAborted:
			return errors.New("codex lifecycle retained a terminal finalizer")
		case sessionActive:
			return errors.New("codex lifecycle registered an active arm as finalizing")
		}
	}
}

func unmatchedPairError(count int) error {
	return fmt.Errorf(
		"codex lifecycle retained %d unmatched repetition snapshot(s)",
		count,
	)
}

// PublicationBoundaryClosed reports whether the listener, every arm session,
// the upstream client and its idle connections, the upstream credential, and
// runner-owned state have all been closed. A live run cannot expose publication
// authority before this returns true, ensuring its committed local proxy
// capability is expired before signed evidence can be obtained.
func (lifecycle *Lifecycle) PublicationBoundaryClosed() bool {
	if lifecycle == nil {
		return false
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.fullyClosed && lifecycle.closed && lifecycle.active == nil &&
		lifecycle.finalizing == nil && lifecycle.client == nil &&
		lifecycle.credential == "" && lifecycle.stateRoot == nil
}

type resolvedConfig struct {
	upstreamURL   *url.URL
	stateRoot     string
	toolboxRoot   string
	listenAddress string
	limits        limits
}

func resolveConfig(config Config) (resolvedConfig, error) {
	if !filepath.IsAbs(config.StateRoot) || filepath.Clean(config.StateRoot) != config.StateRoot ||
		isFilesystemRoot(config.StateRoot) {
		return resolvedConfig{}, errors.New("codex StateRoot must be an absolute, clean, non-root path")
	}
	if !validText(config.ToolboxRoot) || !filepath.IsAbs(config.ToolboxRoot) ||
		filepath.Clean(config.ToolboxRoot) != config.ToolboxRoot ||
		isFilesystemRoot(config.ToolboxRoot) ||
		strings.ContainsRune(config.ToolboxRoot, os.PathListSeparator) {
		return resolvedConfig{}, errors.New(
			"codex ToolboxRoot must be one absolute, clean, non-root PATH directory",
		)
	}
	if pathsOverlap(config.StateRoot, config.ToolboxRoot) {
		return resolvedConfig{}, errors.New("codex StateRoot and ToolboxRoot must be disjoint")
	}
	listenAddress := config.ListenAddress
	if listenAddress == "" {
		listenAddress = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil || host != "127.0.0.1" || port == "" {
		return resolvedConfig{}, errors.New("codex ListenAddress must be 127.0.0.1:PORT")
	}
	if !validCredential(config.UpstreamCredential) {
		return resolvedConfig{}, errors.New("codex upstream credential is empty or contains invalid text")
	}
	if harness.ValidLocalProxyCapability(config.UpstreamCredential) {
		return resolvedConfig{}, errors.New(
			"codex upstream credential must not use the local proxy capability namespace",
		)
	}
	upstream, err := parseUpstreamURL(config.UpstreamURL)
	if err != nil {
		return resolvedConfig{}, err
	}
	resolvedLimits, err := resolveLimits(config)
	if err != nil {
		return resolvedConfig{}, err
	}
	return resolvedConfig{
		stateRoot:     config.StateRoot,
		toolboxRoot:   config.ToolboxRoot,
		listenAddress: listenAddress,
		upstreamURL:   upstream,
		limits:        resolvedLimits,
	}, nil
}

func resolveLimits(config Config) (limits, error) {
	resolved := limits{
		maxRequestBytes:  config.MaxRequestBytes,
		maxResponseBytes: config.MaxResponseBytes,
		maxSSEEventBytes: config.MaxSSEEventBytes,
		maxEvents:        config.MaxEvents,
		maxRequests:      config.MaxRequests,
		maxHeaderBytes:   config.MaxHeaderBytes,
		headerTimeout:    config.ReadHeaderTimeout,
		upstreamTimeout:  config.UpstreamTimeout,
	}
	if resolved.maxRequestBytes == 0 {
		resolved.maxRequestBytes = defaultMaxRequestBytes
	}
	if resolved.maxResponseBytes == 0 {
		resolved.maxResponseBytes = defaultMaxResponseBytes
	}
	if resolved.maxSSEEventBytes == 0 {
		resolved.maxSSEEventBytes = defaultMaxSSEEventBytes
	}
	if resolved.maxEvents == 0 {
		resolved.maxEvents = defaultMaxEvents
	}
	if resolved.maxRequests == 0 {
		resolved.maxRequests = defaultMaxRequests
	}
	if resolved.maxHeaderBytes == 0 {
		resolved.maxHeaderBytes = defaultMaxHeaderBytes
	}
	if resolved.headerTimeout == 0 {
		resolved.headerTimeout = defaultHeaderTimeout
	}
	if resolved.upstreamTimeout == 0 {
		resolved.upstreamTimeout = defaultUpstreamTimeout
	}
	if resolved.maxRequestBytes <= 0 || resolved.maxResponseBytes <= 0 ||
		resolved.maxSSEEventBytes <= 0 || resolved.maxEvents <= 0 ||
		resolved.maxRequests <= 0 || resolved.maxHeaderBytes <= 0 ||
		resolved.headerTimeout <= 0 || resolved.upstreamTimeout <= 0 {
		return limits{}, errors.New("codex proxy limits and timeouts must be positive")
	}
	if resolved.maxResponseBytes > harnesscodex.MaxProviderResponseBodyBytes ||
		resolved.maxRequests > harnesscodex.MaxResponsesTraceRequests ||
		resolved.maxEvents > harnesscodex.MaxResponsesTraceSSEEvents ||
		resolved.maxSSEEventBytes > harnesscodex.MaxResponsesSSEEventBytes {
		return limits{}, errors.New(
			"codex proxy limits exceed Responses trace v4 evidence bounds",
		)
	}
	return resolved, nil
}

func parseUpstreamURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse Codex upstream URL: %w", err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" ||
		parsed.Hostname() == "" {
		return nil, errors.New("codex upstream URL must be an origin without userinfo, path, query, or fragment")
	}
	if parsed.Scheme != "https" &&
		(parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("codex upstream URL must use HTTPS or loopback HTTP")
	}
	parsed.Path = ""
	return parsed, nil
}

func lifecycleIdentity(
	layout RuntimeLayout,
	upstream *url.URL,
	limits limits,
	productionRoute string,
	productionNetwork string,
) (string, error) {
	manifest := struct { //nolint:govet,nolintlint // Field order is the v3 lifecycle identity contract.
		Version              string        `json:"version"`
		Layout               RuntimeLayout `json:"layout"`
		UpstreamOrigin       string        `json:"upstream_origin"`
		ResponsesPath        string        `json:"responses_path"`
		MaxRequestBytes      int64         `json:"max_request_bytes"`
		MaxResponseBytes     int64         `json:"max_response_bytes"`
		MaxSSEEventBytes     int           `json:"max_sse_event_bytes"`
		MaxEvents            int           `json:"max_events"`
		MaxRequests          int           `json:"max_requests"`
		MaxHeaderBytes       int           `json:"max_header_bytes"`
		HeaderTimeoutNanos   int64         `json:"header_timeout_nanos"`
		UpstreamTimeoutNanos int64         `json:"upstream_timeout_nanos"`
		ProductionRoute      string        `json:"production_route"`
		ProductionNetwork    string        `json:"production_network_policy"`
	}{
		Version:              lifecycleVersion,
		Layout:               layout,
		UpstreamOrigin:       upstream.String(),
		ResponsesPath:        responsesPath,
		MaxRequestBytes:      limits.maxRequestBytes,
		MaxResponseBytes:     limits.maxResponseBytes,
		MaxSSEEventBytes:     limits.maxSSEEventBytes,
		MaxEvents:            limits.maxEvents,
		MaxRequests:          limits.maxRequests,
		MaxHeaderBytes:       limits.maxHeaderBytes,
		HeaderTimeoutNanos:   limits.headerTimeout.Nanoseconds(),
		UpstreamTimeoutNanos: limits.upstreamTimeout.Nanoseconds(),
		ProductionRoute:      productionRoute,
		ProductionNetwork:    productionNetwork,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode Codex lifecycle identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return lifecycleVersion + "/sha256:" + hex.EncodeToString(digest[:]), nil
}

func claimStateRoot(path string) (_ *os.Root, resultErr error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat Codex StateRoot: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("codex StateRoot must be a real private directory with mode 0700")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open Codex StateRoot: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = joinCloseError(resultErr, root, "close Codex StateRoot")
		}
	}()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("codex StateRoot changed while opening")
	}
	file, err := root.Open(stateMarkerName)
	switch {
	case err == nil:
		content, readErr := io.ReadAll(io.LimitReader(file, int64(len(stateMarkerData)+1)))
		closeErr := file.Close()
		if readErr != nil {
			return nil, errors.New("read Codex StateRoot marker")
		}
		if closeErr != nil || string(content) != stateMarkerData {
			return nil, errors.Join(errors.New("codex StateRoot marker is invalid"), closeErr)
		}
	case errors.Is(err, fs.ErrNotExist):
		entries, listErr := readDirNames(root)
		if listErr != nil {
			return nil, listErr
		}
		if len(entries) != 0 {
			return nil, errors.New("unclaimed Codex StateRoot must be empty")
		}
		marker, createErr := root.OpenFile(
			stateMarkerName,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if createErr != nil {
			return nil, fmt.Errorf("create Codex StateRoot marker: %w", createErr)
		}
		if _, createErr = marker.WriteString(stateMarkerData); createErr == nil {
			createErr = marker.Sync()
		}
		closeErr := marker.Close()
		if createErr != nil || closeErr != nil {
			return nil, errors.Join(createErr, closeErr)
		}
		if err := syncOSRoot(root); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("open Codex StateRoot marker: %w", err)
	}
	return root, nil
}

func (lifecycle *Lifecycle) resetLayout(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lifecycle.stateRoot == nil {
		return nil
	}
	entries, err := readDirNames(lifecycle.stateRoot)
	if err != nil {
		return err
	}
	allowed := map[string]struct{}{
		stateMarkerName: {},
		"home":          {},
		"codex-home":    {},
		"tmp":           {},
		"config-lock":   {},
	}
	for _, name := range entries {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unexpected entry %q in Codex StateRoot", name)
		}
	}
	for _, name := range []string{"home", "codex-home", "tmp", "config-lock"} {
		if err := lifecycle.stateRoot.RemoveAll(name); err != nil {
			return fmt.Errorf("reset Codex runtime directory %s: %w", name, err)
		}
		if err := lifecycle.stateRoot.Mkdir(name, 0o700); err != nil {
			return fmt.Errorf("create Codex runtime directory %s: %w", name, err)
		}
	}
	codexHome, err := lifecycle.stateRoot.OpenRoot("codex-home")
	if err != nil {
		return fmt.Errorf("open reset CODEX_HOME: %w", err)
	}
	if err := codexHome.Mkdir("sqlite", 0o700); err != nil {
		return joinCloseError(
			fmt.Errorf("create CODEX_SQLITE_HOME: %w", err),
			codexHome,
			"close CODEX_HOME",
		)
	}
	if err := syncOSRoot(codexHome); err != nil {
		return joinCloseError(err, codexHome, "close CODEX_HOME")
	}
	if err := codexHome.Close(); err != nil {
		return fmt.Errorf("close CODEX_HOME: %w", err)
	}
	return syncOSRoot(lifecycle.stateRoot)
}

func (lifecycle *Lifecycle) validateRequest(request genericrunner.ExecutionRequest) error {
	if request.Arm != genericrunner.BaselineArm && request.Arm != genericrunner.CandidateArm {
		return fmt.Errorf("invalid Codex arm %q", request.Arm)
	}
	if request.Repetition < 0 {
		return errors.New("codex repetition must be nonnegative")
	}
	if err := lifecycle.layout.Validate(); err != nil {
		return err
	}
	if lifecycle.production {
		processTimeout := time.Duration(request.Process.TimeoutMillis) * time.Millisecond
		if request.Invocation.TimeoutMillis != request.Process.TimeoutMillis ||
			processTimeout <= 0 || lifecycle.limits.upstreamTimeout != processTimeout {
			return errors.New(
				"production Codex upstream timeout does not match the approved arm timeout",
			)
		}
	}
	wantedEnvironment := lifecycle.layout.Environment()
	if !reflect.DeepEqual(request.Invocation.Environment, wantedEnvironment) ||
		!reflect.DeepEqual(request.Process.Environment, wantedEnvironment) {
		return errors.New("codex launch environment differs from the committed runtime layout")
	}
	if err := validateLayoutArguments(request.Process.Argv, lifecycle.layout); err != nil {
		return err
	}
	if request.Invocation.Model == "" || request.Invocation.Model != request.Invocation.RequestedModel {
		return errors.New("codex requested model is missing or unresolved")
	}
	if request.Arm == genericrunner.BaselineArm && len(request.Invocation.MCPServers) != 0 {
		return errors.New("codex baseline unexpectedly contains an MCP registration")
	}
	if request.Arm == genericrunner.CandidateArm &&
		(len(request.Invocation.MCPServers) != 1 || request.Invocation.MCPServers[0].Name != "scopesifter") {
		return errors.New("codex candidate does not contain exactly the scopesifter registration")
	}
	return nil
}

func validateLayoutArguments(arguments []string, layout RuntimeLayout) error {
	expected := make(map[string]struct{})
	for _, assignment := range layout.ConfigAssignments() {
		expected[assignment] = struct{}{}
	}
	seen := make(map[string]int)
	ownedPrefixes := []string{
		"openai_base_url=",
		"debug.config_lockfile.export_dir=",
		"debug.config_lockfile.save_fields_resolved_from_model_catalog=",
		"shell_environment_policy.set=",
	}
	for index := 0; index < len(arguments); index++ {
		if arguments[index] != "-c" {
			continue
		}
		if index+1 == len(arguments) {
			return errors.New("codex argv ends with a bare -c")
		}
		assignment := arguments[index+1]
		for _, prefix := range ownedPrefixes {
			if strings.HasPrefix(assignment, prefix) {
				if _, ok := expected[assignment]; !ok {
					return fmt.Errorf("codex runtime assignment drifted: %q", assignment)
				}
				seen[assignment]++
			}
		}
		index++
	}
	for assignment := range expected {
		if seen[assignment] != 1 {
			return fmt.Errorf("codex runtime assignment %q appeared %d times", assignment, seen[assignment])
		}
	}
	return nil
}

func providerModel(revision string) (string, error) {
	separator := strings.IndexByte(revision, '@')
	if separator <= 0 || separator == len(revision)-1 || strings.ContainsRune(revision[separator+1:], '@') {
		return "", errors.New("codex model revision is not canonical")
	}
	return revision[separator+1:], nil
}

func digestRequest(request genericrunner.ExecutionRequest) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode approved Codex request: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func cloneExecutionRequest(source genericrunner.ExecutionRequest) genericrunner.ExecutionRequest {
	clone := source
	clone.Invocation = source.Invocation
	clone.Invocation.Environment = cloneStringMap(source.Invocation.Environment)
	clone.Invocation.Arguments = append([]string(nil), source.Invocation.Arguments...)
	clone.Invocation.Prompt = append([]byte(nil), source.Invocation.Prompt...)
	clone.Invocation.MCPServers = make([]harness.MCPServer, len(source.Invocation.MCPServers))
	for index, server := range source.Invocation.MCPServers {
		clone.Invocation.MCPServers[index] = server
		clone.Invocation.MCPServers[index].Environment = cloneStringMap(server.Environment)
		clone.Invocation.MCPServers[index].Arguments = append([]string(nil), server.Arguments...)
	}
	clone.Process = source.Process
	clone.Process.Environment = cloneStringMap(source.Process.Environment)
	clone.Process.Argv = append([]string(nil), source.Process.Argv...)
	clone.Process.Stdin = append([]byte(nil), source.Process.Stdin...)
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func readDirNames(root *os.Root) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open rooted directory: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names, nil
}

func syncOSRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return errors.Join(fmt.Errorf("sync directory: %w", syncErr), closeErr)
	}
	return closeErr
}

func newUpstreamClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("codex upstream redirects are disabled")
		},
	}
}

func newProductionUpstreamClient(
	timeout time.Duration,
	rootCAs *x509.CertPool,
) *http.Client {
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs.Clone(),
			ServerName: productionUpstreamDNS,
			VerifyConnection: func(state tls.ConnectionState) error {
				_, err := productionTLSStateTrace(state)
				return err
			},
		},
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("codex upstream redirects are disabled")
		},
	}
}

func newLocalProxyCapability() (string, error) {
	random := make([]byte, sha256.Size)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("generate local proxy capability")
	}
	defer clear(random)
	return localCapabilityPrefix + base64.RawURLEncoding.EncodeToString(random), nil
}

func validCredential(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback() || host == "localhost"
}
