//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateExternalInvocationBindsExactRoles(t *testing.T) {
	t.Parallel()
	valid := []struct {
		name      string
		arguments []string
	}{
		{name: "mkfs.ext4", arguments: []string{"-q", "-F", "-O", "verity", fsverityImage}},
		{name: "mount", arguments: []string{"-i", "-t", "ext4", "-o", "loop,nosuid,nodev", fsverityImage, fsverityRoot}},
		{name: "umount", arguments: []string{"-i", fsverityRoot}},
	}
	for _, invocation := range valid {
		if err := validateExternalInvocation(invocation.name, invocation.arguments); err != nil {
			t.Fatalf("valid %s invocation rejected: %v", invocation.name, err)
		}
	}
	if err := validateExternalInvocation("bash", []string{"-c", "true"}); err == nil {
		t.Fatal("script runtime accepted as a privileged external executable")
	}
	if err := validateExternalInvocation("mount", []string{"--help"}); err == nil {
		t.Fatal("arguments outside the mount role accepted")
	}
}

func TestValidatePrivilegedSuiteInvocationBindsConfiguration(t *testing.T) {
	t.Parallel()
	delegation := filepath.Join(cgroupRoot, "tokenbench-ci-delegation-v1")
	for _, suite := range configuredPrivilegedSuites() {
		if err := validatePrivilegedSuiteInvocation(suite, delegation); err != nil {
			t.Fatalf("valid suite %s rejected: %v", suite.binary, err)
		}
	}

	mutated := configuredPrivilegedSuites()[0]
	mutated.names = append([]string(nil), mutated.names...)
	mutated.names[0] = "TestUnexpected"
	if err := validatePrivilegedSuiteInvocation(mutated, delegation); err == nil {
		t.Fatal("mutated suite configuration accepted")
	}
	mutated = configuredPrivilegedSuites()[0]
	mutated.binary = "/bin/bash"
	if err := validatePrivilegedSuiteInvocation(mutated, delegation); err == nil {
		t.Fatal("script runtime accepted as a privileged suite")
	}
}

func TestValidateCgroupEntryInvocationBindsDelegatedSuite(t *testing.T) {
	t.Parallel()
	delegation := filepath.Join(cgroupRoot, "tokenbench-ci-delegation-v1")
	suite := configuredPrivilegedSuites()[0]
	arguments := privilegedSuiteArguments(suite)
	if err := validateCgroupEntryInvocation(delegation, suite.binary, arguments); err != nil {
		t.Fatalf("valid cgroup entry rejected: %v", err)
	}
	if err := validateCgroupEntryInvocation(delegation, "/bin/bash", []string{"-c", "true"}); err == nil {
		t.Fatal("script runtime accepted as a cgroup entry")
	}
	mutated := append([]string(nil), arguments...)
	mutated[0] = "-test.run=TestUnexpected"
	if err := validateCgroupEntryInvocation(delegation, suite.binary, mutated); err == nil {
		t.Fatal("mutated delegated test arguments accepted")
	}
}

func TestValidateCommandRunnerUtilityInvocationBindsSelfAndLeafRole(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arguments := []string{commandRunnerUtilityFlag, "sleep-leaf"}
	if err := validateCommandRunnerUtilityInvocation(executable, arguments); err != nil {
		t.Fatalf("valid utility self-invocation rejected: %v", err)
	}
	if err := validateCommandRunnerUtilityInvocation(executable, []string{commandRunnerUtilityFlag, "bash"}); err == nil {
		t.Fatal("mutated utility child role accepted")
	}
	if err := validateCommandRunnerUtilityInvocation("/bin/bash", arguments); err == nil {
		t.Fatal("non-self script runtime accepted as the utility child")
	}
}
