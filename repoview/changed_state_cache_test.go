package repoview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCacheBase = "1111111111111111111111111111111111111111"
	testCacheHead = "2222222222222222222222222222222222222222"
)

func TestChangedStateCacheFilteredPatchesAndChangedOnlyNeverUseGit(t *testing.T) {
	root := t.TempDir()
	writeCacheTestFile(t, root, "changed.go", `package fixture

func Target() { println("changed") }
`)
	writeCacheTestFile(t, root, "renamed.go", `package fixture

func Renamed() {}
`)
	writeCacheTestFile(t, root, "unchanged.go", `package fixture

func Target() { println("unchanged") }
`)
	patches := map[string]string{
		"binary.dat": "diff --git a/binary.dat b/binary.dat\nBinary files differ\n",
		"changed.go": "diff --git a/changed.go b/changed.go\n+func Target() {}\n",
		"deleted.go": "diff --git a/deleted.go b/deleted.go\n-deleted\n",
		"renamed.go": "diff --git a/old.go b/renamed.go\nsimilarity index 100%\n",
	}
	cache := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    testCacheBase,
		HeadCommit:    testCacheHead,
		HeadSubject:   "cache fixture",
		ChangedFiles: []ChangedFileState{
			cacheTestFile("binary.dat", "modified", "", 0, true, nil, patches["binary.dat"]),
			cacheTestFile("changed.go", "modified", "", 0, false, []ChangedLineSpan{{Start: 3, End: 3}}, patches["changed.go"]),
			cacheTestFile("deleted.go", "deleted", "", 0, false, []ChangedLineSpan{{Start: 1, End: 1}}, patches["deleted.go"]),
			cacheTestFile("renamed.go", "renamed", "old.go", 100, false, []ChangedLineSpan{}, patches["renamed.go"]),
		},
		Patch: patches["binary.dat"] + patches["changed.go"] + patches["deleted.go"] + patches["renamed.go"],
	}
	cachePath, cacheSHA256 := writeCacheTestState(t, cache)
	view, err := NewWithChangedStateCache(
		root, cachePath, cacheSHA256, testCacheBase, testCacheHead,
	)
	if err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	marker := filepath.Join(fakeBin, "git-ran")
	writeCacheTestFile(
		t,
		fakeBin,
		"git",
		"#!/bin/sh\n: > "+marker+"\nexit 99\n",
	)
	if err := os.Chmod(filepath.Join(fakeBin, "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	for path, wantPatch := range patches {
		response, err := view.Changed(Options{
			Base:          testCacheBase,
			PathGlobs:     []string{path},
			Return:        ReturnLocations,
			Limit:         20,
			MaxCodeLines:  20,
			MaxPatchLines: 20,
		})
		if err != nil {
			t.Fatalf("changed %s: %v", path, err)
		}
		if response.BaseCommit != testCacheBase || response.HeadCommit != testCacheHead ||
			response.HeadSubject != cache.HeadSubject {
			t.Fatalf("changed %s metadata = %#v", path, response)
		}
		if response.Patch != strings.TrimRight(wantPatch, "\n") || response.PatchTruncated {
			t.Fatalf("changed %s patch = %q, truncated=%t", path, response.Patch, response.PatchTruncated)
		}
		if len(response.Results) != 1 || response.Results[0].Path != path {
			t.Fatalf("changed %s results = %#v", path, response.Results)
		}
		if path == "deleted.go" && response.Results[0].Kind != "file" {
			t.Fatalf("deleted result = %#v", response.Results[0])
		}
	}
	filtered, err := view.Changed(Options{
		Base:          testCacheBase,
		PathGlobs:     []string{"*.go"},
		ExcludeGlobs:  []string{"changed.go", "deleted.go"},
		Return:        ReturnLocations,
		Limit:         20,
		MaxCodeLines:  20,
		MaxPatchLines: 20,
	})
	if err != nil || filtered.Patch != strings.TrimRight(patches["renamed.go"], "\n") {
		t.Fatalf("combined include/exclude filter = %#v, %v", filtered, err)
	}

	find, err := view.Find("Target", Options{
		Base:          testCacheBase,
		ChangedOnly:   true,
		Return:        ReturnLocations,
		Limit:         20,
		MaxCodeLines:  20,
		MaxPatchLines: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(find.Results) == 0 {
		t.Fatal("changed-only find returned no cached changed result")
	}
	for _, result := range find.Results {
		if result.Path != "changed.go" {
			t.Fatalf("changed-only find leaked nonchanged path: %#v", result)
		}
	}
	if _, err := view.gitOutput("status"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("cache-mode Git call error = %v", err)
	}
	if _, err := view.Changed(Options{
		Base: strings.Repeat("3", 40), Return: ReturnLocations,
		Limit: 20, MaxCodeLines: 20, MaxPatchLines: 20,
	}); err == nil || !strings.Contains(err.Error(), "cache base") {
		t.Fatalf("cache-mode wrong-base error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git fallback executed; marker stat error = %v", err)
	}

	// Once authenticated, operations use the in-memory cache rather than
	// reopening a pathname an attacker could replace.
	if err := os.WriteFile(cachePath, []byte(`{"forged":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := view.Changed(Options{
		Base: testCacheBase, PathGlobs: []string{"renamed.go"},
		Return: ReturnLocations, Limit: 20, MaxCodeLines: 20, MaxPatchLines: 20,
	})
	if err != nil || response.Patch != strings.TrimRight(patches["renamed.go"], "\n") {
		t.Fatalf("post-replacement cache result = %#v, %v", response, err)
	}
	writeCacheTestFile(t, root, "changed.go", "package fixture\n")
	if _, err := view.Changed(Options{
		Base: testCacheBase, PathGlobs: []string{"changed.go"},
		Return: ReturnLocations, Limit: 20, MaxCodeLines: 20, MaxPatchLines: 20,
	}); err == nil || !strings.Contains(err.Error(), "configured HEAD source") {
		t.Fatalf("cache/source binding error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git fallback executed after cache/source error; marker stat error = %v", err)
	}
}

func TestChangedStateCacheRejectsDigestPathAndJSONDrift(t *testing.T) {
	root := t.TempDir()
	valid := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    testCacheBase,
		HeadCommit:    testCacheHead,
		HeadSubject:   "valid",
		ChangedFiles:  []ChangedFileState{},
		Patch:         "",
	}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unknown field", raw: []byte(strings.Replace(
			string(validRaw), `{"schema_version"`, `{"unknown":true,"schema_version"`, 1,
		))},
		{name: "trailing value", raw: append(append([]byte(nil), validRaw...), []byte(`{}`)...)},
		{name: "noncanonical whitespace", raw: append(append([]byte(nil), validRaw...), '\n')},
		{name: "schema drift", raw: func() []byte {
			changed := valid
			changed.SchemaVersion = "tokenbench.changed-state-cache/v2"
			raw, marshalErr := json.Marshal(changed)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			return raw
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.json")
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewWithChangedStateCache(
				root, path, sha256Hex(test.raw), testCacheBase, testCacheHead,
			); err == nil {
				t.Fatal("accepted drifted changed-state cache")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, validRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithChangedStateCache(
		root, path, strings.Repeat("0", 64), testCacheBase, testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if _, err := NewWithChangedStateCache(
		root, path, sha256Hex(validRaw), strings.Repeat("3", 40), testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "base mismatch") {
		t.Fatalf("base mismatch error = %v", err)
	}
	if _, err := NewWithChangedStateCache(
		root, path, sha256Hex(validRaw), testCacheBase, strings.Repeat("3", 40),
	); err == nil || !strings.Contains(err.Error(), "head mismatch") {
		t.Fatalf("head mismatch error = %v", err)
	}
	if _, err := NewWithChangedStateCache(
		root, "relative-cache.json", sha256Hex(validRaw), testCacheBase, testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "absolute and canonical") {
		t.Fatalf("relative path error = %v", err)
	}
	symlink := filepath.Join(t.TempDir(), "cache-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithChangedStateCache(
		root, symlink, sha256Hex(validRaw), testCacheBase, testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink path error = %v", err)
	}
	hardlink := filepath.Join(filepath.Dir(path), "cache-hardlink.json")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithChangedStateCache(
		root, hardlink, sha256Hex(validRaw), testCacheBase, testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("hard-linked path error = %v", err)
	}
}

func TestChangedStateCacheExpandedLineBoundary(t *testing.T) {
	root := t.TempDir()
	makeCache := func(end int) ChangedStateCache {
		patch := "diff --git a/huge.go b/huge.go\n"
		return ChangedStateCache{
			SchemaVersion: ChangedStateSchemaVersion,
			BaseCommit:    testCacheBase,
			HeadCommit:    testCacheHead,
			HeadSubject:   "line boundary",
			ChangedFiles: []ChangedFileState{cacheTestFile(
				"huge.go", "modified", "", 0, false,
				[]ChangedLineSpan{{Start: 1, End: end}}, patch,
			)},
			Patch: patch,
		}
	}
	path, digest := writeCacheTestState(t, makeCache(maximumExpandedChangedLines))
	if _, err := NewWithChangedStateCache(
		root, path, digest, testCacheBase, testCacheHead,
	); err != nil {
		t.Fatalf("exact expanded-line boundary rejected: %v", err)
	}
	path, digest = writeCacheTestState(t, makeCache(maximumExpandedChangedLines+1))
	if _, err := NewWithChangedStateCache(
		root, path, digest, testCacheBase, testCacheHead,
	); err == nil || !strings.Contains(err.Error(), "expanded lines") {
		t.Fatalf("over-bound expanded lines error = %v", err)
	}
}

func cacheTestFile(
	path, status, previous string,
	similarity int,
	binary bool,
	lines []ChangedLineSpan,
	patch string,
) ChangedFileState {
	if lines == nil {
		lines = []ChangedLineSpan{}
	}
	return ChangedFileState{
		Path: path, PreviousPath: previous, Status: status,
		Similarity: similarity, Binary: binary, Lines: lines,
		Patch: patch, PatchSHA256: sha256Hex([]byte(patch)),
	}
}

func writeCacheTestState(t *testing.T, cache ChangedStateCache) (string, string) {
	t.Helper()
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "changed-state.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, sha256Hex(raw)
}

func writeCacheTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
