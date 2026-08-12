package workflowrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadTargetAcceptsOnlyOneReviewedToken(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"ci-test", "ci-test\n", "ci-test\r\n"} {
		t.Run(strings.ReplaceAll(content, "\n", "LF"), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "run-file")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			target, err := readTarget(path)
			if err != nil {
				t.Fatal(err)
			}
			if target != "ci-test" {
				t.Fatalf("target = %q", target)
			}
		})
	}
}

func TestReadTargetRejectsExecutableText(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"make ci-test",
		"ci-test && ci-build",
		"ci-test\nci-build\n",
		"ci-test # comment",
		"release",
	} {
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "run-file")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readTarget(path); err == nil {
				t.Fatalf("executable workflow text %q was accepted", content)
			}
		})
	}
}

func TestReadTargetRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realPath := filepath.Join(root, "real")
	if err := os.WriteFile(realPath, []byte("ci-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readTarget(linkPath); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestSanitizedEnvironmentReplacesProcessConfiguration(t *testing.T) {
	t.Parallel()
	got := sanitizedEnvironment([]string{
		"PATH=/usr/bin",
		"MAKEFLAGS=--eval=unsafe",
		"MAKEFILES=unsafe.mk",
		"GOFLAGS=-toolexec=unsafe",
		"GOAUTH=unsafe",
		"GOCACHEPROG=unsafe",
		"GOPROXY=direct",
		"GO_UNREVIEWED=/tmp/tool",
		"CC=/tmp/compiler-wrapper",
		"MALFORMED",
	})
	want := map[string]string{
		"CGO_ENABLED":  "0",
		"GO111MODULE":  "on",
		"GOAUTH":       "off",
		"GOCACHEPROG":  "",
		"GOENV":        "off",
		"GOFLAGS":      "-mod=readonly -trimpath -buildvcs=false",
		"GONOPROXY":    "none",
		"GONOSUMDB":    "",
		"GOPRIVATE":    "",
		"GOPROXY":      "https://proxy.golang.org",
		"GOSUMDB":      "sum.golang.org",
		"GOTOOLCHAIN":  "local",
		"GOVCS":        "*:off",
		"GOWORK":       "off",
		"GNUMAKEFLAGS": "",
		"MAKEFILES":    "",
		"MAKEFLAGS":    "",
		"MFLAGS":       "",
		"PATH":         "/usr/bin",
	}
	if len(got) != len(want) {
		t.Fatalf("environment length = %d, want %d: %#v", len(got), len(want), got)
	}
	for _, entry := range got {
		name, value, found := strings.Cut(entry, "=")
		if !found || want[name] != value {
			t.Fatalf("unexpected environment entry %q", entry)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing environment values: %#v", want)
	}
}

func TestMakeArgumentsAreExact(t *testing.T) {
	t.Parallel()
	want := []string{
		"--no-builtin-rules",
		"--no-builtin-variables",
		"-f",
		"Makefile",
		"--",
		"ci-test",
	}
	if got := makeArguments("ci-test"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Make arguments = %#v, want %#v", got, want)
	}
}

func TestRunInvokesReviewedTrackedMakeTarget(t *testing.T) {
	t.Parallel()
	root := newWorkflowRepository(t)
	writeWorkflowTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tmkdir -p bin\n")
	runFile := filepath.Join(t.TempDir(), "run-file")
	if err := os.WriteFile(runFile, []byte("ci-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), root, runFile, nil, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "bin")); err != nil || !info.IsDir() {
		t.Fatalf("reviewed target did not create bin: %v", err)
	}
}

func TestRunRejectsUnsafeTrackedMakeGraphBeforeTargetExecution(t *testing.T) {
	t.Parallel()
	root := newWorkflowRepository(t)
	writeWorkflowTracked(
		t,
		root,
		"Makefile",
		"include make/unsafe.mk\n.PHONY: ci-test\nci-test:\n\tmkdir -p bin\n",
	)
	writeWorkflowTracked(
		t,
		root,
		"make/unsafe.mk",
		".PHONY: unsafe\nunsafe:\n\tbash -c true\n",
	)
	runFile := filepath.Join(t.TempDir(), "run-file")
	if err := os.WriteFile(runFile, []byte("ci-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), root, runFile, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unsafe Make recipe") {
		t.Fatalf("unsafe tracked Make graph error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Fatalf("target ran before the tracked Make graph was rejected: %v", err)
	}
}

func TestRunRejectsProjectScriptProcessBeforeTargetExecution(t *testing.T) {
	t.Parallel()
	root := newWorkflowRepository(t)
	writeWorkflowTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tmkdir -p bin\n")
	writeWorkflowTracked(t, root, "unsafe.go", `package unsafe

import "os/exec"

func run() { _ = exec.Command("bash", "-c", "true") }
`)
	runFile := filepath.Join(t.TempDir(), "run-file")
	if err := os.WriteFile(runFile, []byte("ci-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), root, runFile, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "script-runtime process execution") {
		t.Fatalf("unsafe project process error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Fatalf("target ran before project policy rejection: %v", err)
	}
}

func newWorkflowRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}

func writeWorkflowTracked(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "add", "--", path)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
}
