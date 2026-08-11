package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/runner"
	runnercodex "github.com/scopesifter/scopesifter/benchmarks/tokenbench/runner/codex"
)

type fakeLifecycle struct{}

func (fakeLifecycle) Identity() string {
	return "tokenbench.codex-runner/codex-cli-v0.144.0/v3/sha256:" +
		"0000000000000000000000000000000000000000000000000000000000000000"
}

func (fakeLifecycle) BeginArm(
	context.Context,
	runner.ExecutionRequest,
) (runner.ArmSession, error) {
	return nil, nil
}

type lifecycleWrapper struct{ runner.Lifecycle }

func TestGenericConstructionIsNeverPublishable(t *testing.T) {
	for _, lifecycle := range []runner.Lifecycle{nil, fakeLifecycle{}} {
		executor, err := runner.New(runner.Config{Lifecycle: lifecycle})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if executor.Publishable() {
			t.Fatal("generic construction obtained publication provenance")
		}
	}
	if _, err := runner.NewConformant(runner.Config{Lifecycle: fakeLifecycle{}}); err == nil {
		t.Fatal("fake lifecycle obtained conformant construction")
	}
}

func TestOnlyConcreteBuiltInCodexLifecycleIsPublishable(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := runnercodex.NewProduction(runnercodex.ProductionConfig{
		StateRoot:          stateRoot,
		ToolboxRoot:        filepath.Join(t.TempDir(), "toolbox"),
		UpstreamCredential: "offline-provenance-test-credential",
		UpstreamTimeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("codex.New(): %v", err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Close(context.Background()); err != nil {
			t.Errorf("Lifecycle.Close(): %v", err)
		}
	})

	generic, err := runner.New(runner.Config{Lifecycle: lifecycle})
	if err != nil {
		t.Fatalf("runner.New(): %v", err)
	}
	if generic.Publishable() {
		t.Fatal("generic constructor self-attested the built-in lifecycle")
	}

	conformant, err := runner.NewConformant(runner.Config{Lifecycle: lifecycle})
	if err != nil {
		if !strings.Contains(err.Error(), "initialize runner cgroup containment") {
			t.Fatalf("runner.NewConformant(): %v", err)
		}
		if os.Getenv("TOKENBENCH_REQUIRE_PRIVILEGED_TESTS") == "1" {
			t.Fatalf("required conformant cgroup-v2 containment unavailable: %v", err)
		}
		// Normal CI need not provide an exclusive, bounded cgroup delegation.
		// Reaching this stage proves the exact production lifecycle passed the
		// unforgeable provenance gate; construction then fails closed on host
		// containment instead of relaxing it.
		return
	}
	t.Cleanup(func() {
		if err := conformant.Close(context.Background()); err != nil {
			t.Errorf("Executor.Close(): %v", err)
		}
	})
	if !conformant.Publishable() {
		t.Fatal("built-in Codex lifecycle did not receive conformant provenance")
	}
	if conformant.Identity() == generic.Identity() {
		t.Fatal("executor identity did not commit the construction provenance")
	}

	if _, err := runner.NewConformant(runner.Config{
		Lifecycle: lifecycleWrapper{Lifecycle: lifecycle},
	}); err == nil {
		t.Fatal("wrapper around built-in lifecycle obtained conformant provenance")
	}
}

func TestConcreteNonProductionCodexLifecycleIsNotPublishable(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := runnercodex.New(runnercodex.Config{
		StateRoot:          stateRoot,
		ToolboxRoot:        filepath.Join(t.TempDir(), "toolbox"),
		UpstreamURL:        "https://example.invalid",
		UpstreamCredential: "offline-nonproduction-test-credential",
		UpstreamTimeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lifecycle.Close(context.Background()) })
	if _, err := runner.NewConformant(runner.Config{Lifecycle: lifecycle}); err == nil ||
		!strings.Contains(err.Error(), "built-in Codex lifecycle") {
		t.Fatalf("nonproduction concrete lifecycle passed provenance gate: %v", err)
	}
}
