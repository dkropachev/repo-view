package main

import (
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
