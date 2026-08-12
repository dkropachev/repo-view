package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidatePinnedImage(t *testing.T) {
	t.Parallel()
	valid := "example.invalid/tool@sha256:" + strings.Repeat("a", 64)
	if err := validatePinnedImage(valid); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	for _, image := range []string{
		"example.invalid/tool:latest",
		"example.invalid/tool@sha256:abcd",
		"example.invalid/tool@sha256:" + strings.Repeat("A", 64),
		"@sha256:" + strings.Repeat("a", 64),
		valid + "@sha256:" + strings.Repeat("b", 64),
	} {
		t.Run(image, func(t *testing.T) {
			t.Parallel()
			if err := validatePinnedImage(image); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}

func TestValidateTestOutput(t *testing.T) {
	t.Parallel()
	required := []string{"TestOne", "TestTwo"}
	output := []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n=== RUN   TestTwo\n--- PASS: TestTwo (0.01s)\nPASS\n")
	if err := validateTestOutput(output, required); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
}

func TestValidateTestOutputFailsClosed(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"missing final pass": "=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n",
		"skip":               "=== RUN   TestOne\n--- SKIP: TestOne (0.00s)\nPASS\n",
		"subtest skip":       "=== RUN   TestOne\n=== RUN   TestOne/sub\n    --- SKIP: TestOne/sub (0.00s)\n--- PASS: TestOne (0.00s)\nPASS\n",
		"unexpected":         "--- PASS: TestOne (0.00s)\n--- PASS: TestOther (0.00s)\nPASS\n",
		"duplicate":          "--- PASS: TestOne (0.00s)\n--- PASS: TestOne (0.00s)\nPASS\n",
		"failure":            "--- FAIL: TestOne (0.00s)\nFAIL\n",
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateTestOutput([]byte(output), []string{"TestOne"}); err == nil {
				t.Fatal("invalid output accepted")
			}
		})
	}
}

func TestReplaceEnvironment(t *testing.T) {
	t.Parallel()
	got := replaceEnvironment([]string{"A=old", "UNCHANGED=yes", "A=duplicate"}, map[string]string{
		"A": "new",
		"B": "added",
	})
	want := []string{"UNCHANGED=yes", "A=new", "B=added"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestValidateContainerEngineInvocationBindsRoleAndArguments(t *testing.T) {
	t.Parallel()
	image := "example.invalid/tool@sha256:" + strings.Repeat("a", 64)
	arguments := privilegedContainerArguments(
		"/workspace/repository",
		"/tmp/tokenbench-test-binaries",
		image,
		"mnt:[123]",
		"cgroup:[456]",
	)
	if err := validateContainerEngineInvocation("docker", arguments); err != nil {
		t.Fatalf("valid container invocation rejected: %v", err)
	}

	mutated := append([]string(nil), arguments...)
	mutated[len(mutated)-1] = "bash"
	if err := validateContainerEngineInvocation("docker", mutated); err == nil {
		t.Fatal("mutated container command accepted")
	}
	if err := validateContainerEngineInvocation("make", arguments); err == nil {
		t.Fatal("native dispatcher accepted as a container engine")
	}
	if err := validateContainerEngineInvocation("bash", arguments); err == nil {
		t.Fatal("script runtime accepted as a container engine")
	}
}

func TestValidateBuildInvocationBindsPackageAndEnvironment(t *testing.T) {
	t.Parallel()
	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	binaryDirectory := filepath.Join(os.TempDir(), "tokenbench-build-validation")
	environment := privilegedBuildEnvironment(os.Environ(), repositoryRoot)
	arguments := []string{
		"test", "-mod=readonly", "-c", "-o",
		filepath.Join(binaryDirectory, "runner.test"),
		"./benchmarks/tokenbench/runner",
	}
	if err := validateBuildInvocation("go", repositoryRoot, binaryDirectory, environment, arguments); err != nil {
		t.Fatalf("valid build invocation rejected: %v", err)
	}

	mutated := append([]string(nil), arguments...)
	mutated[5] = "./internal/processpolicy"
	if err := validateBuildInvocation("go", repositoryRoot, binaryDirectory, environment, mutated); err == nil {
		t.Fatal("unapproved build package accepted")
	}
	if err := validateBuildInvocation("bash", repositoryRoot, binaryDirectory, environment, arguments); err == nil {
		t.Fatal("script runtime accepted as the build executable")
	}
	unsafeEnvironment := replaceEnvironment(environment, map[string]string{"GOFLAGS": "-toolexec=/tmp/tool"})
	if err := validateBuildInvocation("go", repositoryRoot, binaryDirectory, unsafeEnvironment, arguments); err == nil {
		t.Fatal("execution-injecting Go flags accepted")
	}
	unsafeEnvironment = slices.Clone(environment)
	unsafeEnvironment = append(unsafeEnvironment, "GOCACHEPROG=/bin/bash")
	if err := validateBuildInvocation("go", repositoryRoot, binaryDirectory, unsafeEnvironment, arguments); err == nil {
		t.Fatal("ambient Go helper program accepted")
	}
}
