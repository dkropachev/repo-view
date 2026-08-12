package runner

import (
	"errors"
	"reflect"
	"strings"
)

const ConformancePolicySchemaVersion = "tokenbench.runner-conformance-policy/v3"

type constructionMode string

const (
	genericConstruction         constructionMode = "extension"
	conformantCodexConstruction constructionMode = "builtin-codex-v0.144.0"

	codexLifecyclePackage         = "github.com/yapless/scopesifter/benchmarks/tokenbench/runner/codex"
	codexIdentityPrefix           = "tokenbench.codex-runner/codex-cli-v0.144.0/v3/sha256:"
	productionRoutePolicy         = "openai-api/v1:https://api.openai.com/v1"
	productionNetworkPolicyPrefix = "go-system-roots/v1;proxy=disabled;ambient-overrides=forbidden/sha256:"
)

// Publishable reports whether this executor was constructed through the
// code-owned conformant path with the exact built-in Codex lifecycle. Generic
// New construction is never publishable, even when handed that lifecycle.
func (executor *Executor) Publishable() bool {
	if executor == nil {
		return false
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.publishableLocked()
}

// publishableLocked reports publication eligibility at one executor-state
// instant. The caller must hold executor.mu so Close cannot detach the policy
// descriptors between this check and ConformancePolicy's defensive copy.
func (executor *Executor) publishableLocked() bool {
	return !executor.closed &&
		executor.construction == conformantCodexConstruction &&
		executor.containment != nil &&
		executor.containment.requireBounded &&
		executor.fullFilesystemPolicy &&
		executor.exactNetworkBoundary &&
		executor.pidNamespace &&
		executor.lifecycleIdentity != "" &&
		exactCodexLifecycle(executor.lifecycle) &&
		executor.lifecycle.Identity() == executor.lifecycleIdentity
}

// ConformancePolicy returns a defensive copy of the exact policy enforced by
// a publishable executor. Callers must compare it to separate immutable input
// authority; this value cannot make a generic executor publishable.
func (executor *Executor) ConformancePolicy() (ConformancePolicyIdentity, error) {
	if executor == nil {
		return ConformancePolicyIdentity{}, errors.New("executor is not publishable")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if !executor.publishableLocked() {
		return ConformancePolicyIdentity{}, errors.New("executor is not publishable")
	}
	if executor.commonMCP == nil || executor.armInit == nil {
		return ConformancePolicyIdentity{}, errors.New("executor conformance images are unavailable")
	}
	return ConformancePolicyIdentity{
		SchemaVersion:             ConformancePolicySchemaVersion,
		ReadOnlyPaths:             append([]string(nil), executor.readOnlyPaths...),
		ExecutablePaths:           append([]string(nil), executor.executablePaths...),
		CommonMCPExecutable:       executor.commonMCP.path,
		CommonMCPExecutableSHA256: executor.commonMCP.digest,
		ArmInitExecutableSHA256:   executor.armInit.digest,
	}, nil
}

// PublicationBoundaryClosed is true only after this executor proved all arm
// descendants gone and the exact built-in lifecycle closed its listener,
// sessions, state descriptors, and upstream credential. Evidence acquisition
// uses this live gate so the committed local proxy capability is already
// expired before a signer can obtain the run snapshot.
func (executor *Executor) PublicationBoundaryClosed() bool {
	if executor == nil {
		return false
	}
	executor.mu.Lock()
	closed := executor.fullyClosed && executor.closed && len(executor.active) == 0
	lifecycle := executor.lifecycle
	executor.mu.Unlock()
	if !closed || !exactCodexLifecycle(lifecycle) {
		return false
	}
	boundary, ok := lifecycle.(interface {
		PublicationBoundaryClosed() bool
	})
	return ok && boundary.PublicationBoundaryClosed()
}

func exactCodexLifecycle(lifecycle Lifecycle) bool {
	if lifecycle == nil {
		return false
	}
	value := reflect.ValueOf(lifecycle)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return false
	}
	element := value.Type().Elem()
	if element.Kind() != reflect.Struct || element.Name() != "Lifecycle" ||
		element.PkgPath() != codexLifecyclePackage {
		return false
	}
	identity := lifecycle.Identity()
	digest, ok := strings.CutPrefix(identity, codexIdentityPrefix)
	if !ok || !validSHA256(digest) {
		return false
	}
	production, ok := lifecycle.(interface {
		ProductionRouteIdentity() (string, bool)
		ProductionNetworkPolicyIdentity() (string, bool)
	})
	if !ok {
		return false
	}
	route, routeOK := production.ProductionRouteIdentity()
	network, networkOK := production.ProductionNetworkPolicyIdentity()
	networkDigest, networkCanonical := strings.CutPrefix(network, productionNetworkPolicyPrefix)
	return routeOK && networkOK && route == productionRoutePolicy &&
		networkCanonical && validSHA256(networkDigest)
}
