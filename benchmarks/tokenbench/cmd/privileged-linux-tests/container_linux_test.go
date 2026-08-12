//go:build linux

package main

import (
	"maps"
	"slices"
	"testing"
)

func TestCommandRunnerProofOwnsFirstPrivilegedSuite(t *testing.T) {
	t.Parallel()
	if len(privilegedSuites) < 2 {
		t.Fatal("privileged suite list omitted its isolated command-runner proof")
	}
	first := privilegedSuites[0]
	if first.binary != "/tokenbench-tests/runner.test" || !first.delegated ||
		!slices.Equal(first.names, []string{"TestPrivilegedGoCommandRunnerDiscoveryPath"}) {
		t.Fatalf("first privileged suite is not the isolated command-runner proof: %+v", first)
	}
	wantEnvironment := map[string]string{
		commandRunnerImageEnvironment:   "/tokenbench-tests/tokenbench",
		commandRunnerUtilityEnvironment: "/tokenbench-tests/privileged-linux-tests",
	}
	if !maps.Equal(first.environment, wantEnvironment) {
		t.Fatalf("command-runner fixture environment = %v, want %v", first.environment, wantEnvironment)
	}
	for _, suite := range privilegedSuites[1:] {
		if slices.Contains(suite.names, "TestPrivilegedGoCommandRunnerDiscoveryPath") {
			t.Fatal("command-runner proof was duplicated after another cgroup fixture")
		}
	}
}
