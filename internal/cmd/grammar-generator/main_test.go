package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"-help"}, &stderr); err != nil {
		t.Fatalf("run(-help) error = %v", err)
	}
	if !strings.Contains(stderr.String(), "grammar-generator -language") {
		t.Fatalf("help output = %q, want usage", stderr.String())
	}
}

func TestRunRequiresSource(t *testing.T) {
	t.Setenv("GRAMMAR_SOURCE", "")

	err := run(context.Background(), []string{"-language", "csharp"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "source directory is required") {
		t.Fatalf("run() error = %v, want missing-source error", err)
	}
}

func TestRunRejectsPositionalArguments(t *testing.T) {
	var stderr bytes.Buffer
	err := run(context.Background(), []string{"unexpected"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("run() error = %v, want positional-argument error", err)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}
