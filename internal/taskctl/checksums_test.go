package taskctl

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSHA256SUMSCanonicalClosure(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "zeta.txt", []byte("zeta\n"))
	writeChecksumFixture(t, root, "nested/alpha.txt", []byte("alpha\n"))
	writeChecksumFixture(t, root, "a space.txt", []byte("space\n"))
	writeChecksumFixture(t, root, checksumManifestName, []byte("obsolete manifest"))
	writeChecksumFixture(t, root, "scratch/ignored.txt", []byte("ignored"))

	got, err := BuildSHA256SUMS(root, "scratch")
	if err != nil {
		t.Fatalf("BuildSHA256SUMS() error = %v", err)
	}
	want := checksumLine("a space.txt", []byte("space\n")) +
		checksumLine("nested/alpha.txt", []byte("alpha\n")) +
		checksumLine("zeta.txt", []byte("zeta\n"))
	if string(got) != want {
		t.Fatalf("BuildSHA256SUMS() = %q, want %q", got, want)
	}
	second, err := BuildSHA256SUMS(root, "scratch")
	if err != nil {
		t.Fatalf("second BuildSHA256SUMS() error = %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatal("BuildSHA256SUMS() is not deterministic")
	}
	if err := ValidateSHA256SUMS(root, got, "scratch"); err != nil {
		t.Fatalf("ValidateSHA256SUMS() error = %v", err)
	}
}

func TestValidateSHA256SUMSDetectsClosureDrift(t *testing.T) {
	newFixture := func(t *testing.T) (string, []byte) {
		t.Helper()
		root := t.TempDir()
		writeChecksumFixture(t, root, "a.txt", []byte("a"))
		writeChecksumFixture(t, root, "b.txt", []byte("b"))
		manifest, err := BuildSHA256SUMS(root)
		if err != nil {
			t.Fatal(err)
		}
		return root, manifest
	}

	t.Run("missing", func(t *testing.T) {
		root, manifest := newFixture(t)
		if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
			t.Fatal(err)
		}
		assertChecksumErrorContains(t, ValidateSHA256SUMS(root, manifest), `missing file "a.txt"`)
	})
	t.Run("extra", func(t *testing.T) {
		root, manifest := newFixture(t)
		writeChecksumFixture(t, root, "extra.txt", []byte("extra"))
		assertChecksumErrorContains(t, ValidateSHA256SUMS(root, manifest), `extra file "extra.txt"`)
	})
	t.Run("changed", func(t *testing.T) {
		root, manifest := newFixture(t)
		writeChecksumFixture(t, root, "b.txt", []byte("changed"))
		assertChecksumErrorContains(t, ValidateSHA256SUMS(root, manifest), `changed file "b.txt"`)
	})
}

func TestValidateSHA256SUMSRejectsNoncanonicalManifest(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "a.txt", []byte("a"))
	a := checksumLine("a.txt", []byte("a"))
	b := checksumLine("b.txt", []byte("b"))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("a")))
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "uppercase digest", manifest: strings.ToUpper(digest) + "  a.txt\n", want: "invalid lowercase SHA-256"},
		{name: "one separator space", manifest: digest + " a.txt\n", want: "invalid SHA256SUMS line"},
		{name: "no final newline", manifest: strings.TrimSuffix(a, "\n"), want: "must end with a newline"},
		{name: "absolute path", manifest: digest + "  /a.txt\n", want: "unsafe path"},
		{name: "parent traversal", manifest: digest + "  ../a.txt\n", want: "unsafe path"},
		{name: "manifest self entry", manifest: checksumLine(checksumManifestName, []byte("x")), want: "excluded path"},
		{name: "duplicate", manifest: a + a, want: "duplicate path"},
		{name: "not ordered", manifest: b + a, want: "not in bytewise order"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertChecksumErrorContains(
				t,
				ValidateSHA256SUMS(root, []byte(test.manifest)),
				test.want,
			)
		})
	}
}

func TestSHA256SUMSRejectsSymlinksAndUnsafeRoots(t *testing.T) {
	t.Run("filesystem root", func(t *testing.T) {
		filesystemRoot := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
		_, err := BuildSHA256SUMS(filesystemRoot)
		assertChecksumErrorContains(t, err, "must not be a filesystem root")
	})
	t.Run("included symlink", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "target.txt", []byte("target"))
		if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildSHA256SUMS(root)
		assertChecksumErrorContains(t, err, "symlink is not admitted")
	})
	t.Run("manifest symlink", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "target.txt", []byte("target"))
		if err := os.Symlink("target.txt", filepath.Join(root, checksumManifestName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildSHA256SUMS(root)
		assertChecksumErrorContains(t, err, "SHA256SUMS output is not a regular non-symlink file")
	})
	t.Run("symlink root", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildSHA256SUMS(link)
		assertChecksumErrorContains(t, err, "must not traverse symlinks")
	})
	t.Run("symlink root ancestor", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		root := filepath.Join(target, "nested")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildSHA256SUMS(filepath.Join(link, "nested"))
		assertChecksumErrorContains(t, err, "must not traverse symlinks")
	})
	t.Run("unsafe filename", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "line\nbreak", []byte("unsafe"))
		_, err := BuildSHA256SUMS(root)
		assertChecksumErrorContains(t, err, "unsafe checksum path")
	})
}

func TestSHA256SUMSUsesPinnedRootAcrossPathExchange(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	substitute := filepath.Join(parent, "substitute")
	displaced := filepath.Join(parent, "displaced")
	for _, directory := range []string{root, substitute} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeChecksumFixture(t, root, "trusted.txt", []byte("trusted\n"))
	writeChecksumFixture(t, substitute, "substitute.txt", []byte("substitute\n"))

	exclusions, err := newChecksumExclusions(nil, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	restore := func() error {
		if !swapped {
			return nil
		}
		if err := os.Rename(root, substitute); err != nil {
			return err
		}
		if err := os.Rename(displaced, root); err != nil {
			return err
		}
		swapped = false
		return nil
	}
	t.Cleanup(func() { _ = restore() })
	entries, err := collectChecksumEntriesWithExclusionsAndHooks(
		root,
		exclusions,
		defaultChecksumLimits,
		checksumTraversalHooks{
			afterRootOpen: func() error {
				if err := os.Rename(root, displaced); err != nil {
					return err
				}
				if err := os.Rename(substitute, root); err != nil {
					_ = os.Rename(displaced, root)
					return err
				}
				swapped = true
				return nil
			},
			beforeRootRevalidate: func(pass int) error {
				if pass == 1 {
					return restore()
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := marshalChecksumEntries(entries, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	want := checksumLine("trusted.txt", []byte("trusted\n"))
	if string(manifest) != want {
		t.Fatalf("manifest after root exchange = %q, want %q", manifest, want)
	}
}

func TestSHA256SUMSRejectsUnrestoredRootPathExchange(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	substitute := filepath.Join(parent, "substitute")
	displaced := filepath.Join(parent, "displaced")
	for _, directory := range []string{root, substitute} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeChecksumFixture(t, root, "trusted.txt", []byte("trusted\n"))
	writeChecksumFixture(t, substitute, "substitute.txt", []byte("substitute\n"))
	exclusions, err := newChecksumExclusions(nil, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	restore := func() {
		if !swapped {
			return
		}
		_ = os.Rename(root, substitute)
		_ = os.Rename(displaced, root)
		swapped = false
	}
	t.Cleanup(restore)
	_, err = collectChecksumEntriesWithExclusionsAndHooks(
		root,
		exclusions,
		defaultChecksumLimits,
		checksumTraversalHooks{afterRootOpen: func() error {
			if err := os.Rename(root, displaced); err != nil {
				return err
			}
			if err := os.Rename(substitute, root); err != nil {
				_ = os.Rename(displaced, root)
				return err
			}
			swapped = true
			return nil
		}},
	)
	assertChecksumErrorContains(t, err, "checksum root changed while checksumming")
}

func TestChecksumHelpersDoNotClaimAtomicWritableTreeSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	writeChecksumFixture(t, root, "a.txt", []byte("first"))
	writeChecksumFixture(t, root, "z.txt", []byte("later"))
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	exclusions, err := newChecksumExclusions(nil, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectChecksumEntriesWithExclusionsAndHooks(
		root,
		exclusions,
		defaultChecksumLimits,
		checksumTraversalHooks{beforeRootRevalidate: func(pass int) error {
			if pass != 2 {
				return nil
			}
			if err := os.WriteFile(path, []byte("other"), 0o644); err != nil {
				return err
			}
			return os.Chtimes(path, original.ModTime(), original.ModTime())
		}},
	)
	if err != nil {
		t.Fatalf("point-per-file capture unexpectedly failed: %v", err)
	}
	manifest, err := marshalChecksumEntries(entries, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), checksumLine("a.txt", []byte("first"))) {
		t.Fatalf("capture did not retain the earlier observed bytes: %q", manifest)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != "other" {
		t.Fatalf("current file = %q, %v; want later bytes", current, err)
	}
}

func TestSHA256SUMSUsesPinnedNestedDirectoryAcrossPathExchange(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	nested := filepath.Join(root, "nested")
	substitute := filepath.Join(parent, "substitute")
	displaced := filepath.Join(parent, "displaced")
	for _, directory := range []string{nested, substitute} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeChecksumFixture(t, nested, "trusted.txt", []byte("trusted\n"))
	writeChecksumFixture(t, substitute, "substitute.txt", []byte("substitute\n"))
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	exclusions, err := newChecksumExclusions(nil, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	restore := func() error {
		if !swapped {
			return nil
		}
		if err := os.Rename(nested, substitute); err != nil {
			return err
		}
		if err := os.Rename(displaced, nested); err != nil {
			return err
		}
		if err := os.Chtimes(root, rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
			return err
		}
		swapped = false
		return nil
	}
	t.Cleanup(func() { _ = restore() })
	entries, err := collectChecksumEntriesWithExclusionsAndHooks(
		root,
		exclusions,
		defaultChecksumLimits,
		checksumTraversalHooks{
			afterDirectoryOpen: func(pass int, path string) error {
				if pass != 1 || path != "nested" {
					return nil
				}
				if err := os.Rename(nested, displaced); err != nil {
					return err
				}
				if err := os.Rename(substitute, nested); err != nil {
					_ = os.Rename(displaced, nested)
					return err
				}
				swapped = true
				return nil
			},
			beforeDirectoryRevalidate: func(pass int, path string) error {
				if pass == 1 && path == "nested" {
					return restore()
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := marshalChecksumEntries(entries, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	want := checksumLine("nested/trusted.txt", []byte("trusted\n"))
	if string(manifest) != want {
		t.Fatalf("manifest after nested exchange = %q, want %q", manifest, want)
	}
}

func TestSHA256SUMSRejectsMultiplyLinkedFiles(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "file.txt", []byte("file\n"))
	alias := filepath.Join(t.TempDir(), "alias.txt")
	if err := os.Link(filepath.Join(root, "file.txt"), alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := BuildSHA256SUMS(root)
	assertChecksumErrorContains(t, err, `checksum file "file.txt" must have exactly one hard link`)
	assertChecksumErrorContains(
		t,
		ValidateSHA256SUMS(root, []byte(checksumLine("file.txt", []byte("file\n")))),
		`checksum file "file.txt" must have exactly one hard link`,
	)
}

func TestSHA256SUMSResourceBounds(t *testing.T) {
	baseLimits := checksumLimits{
		maxEntries:       10,
		maxFiles:         10,
		maxPathBytes:     100,
		maxDepth:         10,
		maxFileBytes:     100,
		maxTotalBytes:    100,
		maxManifestBytes: 1_000,
	}

	t.Run("file count", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "a", []byte("a"))
		writeChecksumFixture(t, root, "b", []byte("b"))
		limits := baseLimits
		limits.maxFiles = 1
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "exceeds 1 regular files")
	})
	t.Run("filesystem entry count", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		limits := baseLimits
		limits.maxEntries = 1
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "exceeds 1 filesystem entries")
	})
	t.Run("individual file bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "large", []byte("12345"))
		limits := baseLimits
		limits.maxFileBytes = 4
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "exceeds 4 bytes")
	})
	t.Run("total file bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "a", []byte("123"))
		writeChecksumFixture(t, root, "b", []byte("456"))
		limits := baseLimits
		limits.maxTotalBytes = 5
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "exceeds 5 file bytes")
	})
	t.Run("manifest bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "a", []byte("a"))
		limits := baseLimits
		limits.maxManifestBytes = 1
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "SHA256SUMS exceeds 1 bytes")
		assertChecksumErrorContains(t, validateSHA256SUMS(root, []byte("xx"), nil, limits), "SHA256SUMS exceeds 1 bytes")
	})
	t.Run("path bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "long-name", []byte("a"))
		limits := baseLimits
		limits.maxPathBytes = 4
		_, err := buildSHA256SUMS(root, nil, limits)
		assertChecksumErrorContains(t, err, "path exceeds 4 bytes")
	})
}

func TestChecksumExclusionsResourceBoundsAndContainment(t *testing.T) {
	baseLimits := checksumLimits{
		maxEntries:       10,
		maxFiles:         10,
		maxPathBytes:     20,
		maxDepth:         3,
		maxFileBytes:     100,
		maxTotalBytes:    100,
		maxManifestBytes: 1_000,
	}

	t.Run("entry count", func(t *testing.T) {
		limits := baseLimits
		limits.maxEntries = 1
		_, err := buildSHA256SUMS(t.TempDir(), []string{"first", "second"}, limits)
		assertChecksumErrorContains(t, err, "exclusion set exceeds 1 paths")
	})
	t.Run("file count", func(t *testing.T) {
		limits := baseLimits
		limits.maxFiles = 1
		_, err := buildSHA256SUMS(t.TempDir(), []string{"first", "second"}, limits)
		assertChecksumErrorContains(t, err, "exclusion set exceeds 1 paths")
	})
	t.Run("duplicate paths deduplicate", func(t *testing.T) {
		limits := baseLimits
		limits.maxEntries = 1
		limits.maxFiles = 1
		exclusions, err := newChecksumExclusions(
			[]string{"scratch", "scratch", checksumManifestName},
			limits,
		)
		if err != nil {
			t.Fatalf("newChecksumExclusions() error = %v", err)
		}
		if !exclusions.contains("scratch/result.txt") {
			t.Fatal("deduplicated exclusion does not contain its descendant")
		}
	})
	t.Run("path bytes", func(t *testing.T) {
		limits := baseLimits
		limits.maxPathBytes = 4
		_, err := buildSHA256SUMS(t.TempDir(), []string{"lengthy"}, limits)
		assertChecksumErrorContains(t, err, "path exceeds 4 bytes")
	})
	t.Run("path depth", func(t *testing.T) {
		limits := baseLimits
		limits.maxDepth = 2
		_, err := buildSHA256SUMS(t.TempDir(), []string{"one/two/three"}, limits)
		assertChecksumErrorContains(t, err, "path exceeds depth 2")
	})
	t.Run("exact and ancestor membership", func(t *testing.T) {
		exclusions, err := newChecksumExclusions([]string{"cache/work"}, baseLimits)
		if err != nil {
			t.Fatalf("newChecksumExclusions() error = %v", err)
		}
		for _, path := range []string{checksumManifestName, "cache/work", "cache/work/result.txt"} {
			if !exclusions.contains(path) {
				t.Errorf("contains(%q) = false, want true", path)
			}
		}
		for _, path := range []string{"cache", "cache/worker", "other/cache/work"} {
			if exclusions.contains(path) {
				t.Errorf("contains(%q) = true, want false", path)
			}
		}
	})
}

func TestBuildPointerSHA256SUMSHashesOnlyExplicitFiles(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "z.txt", []byte("z\n"))
	writeChecksumFixture(t, root, "nested/a.txt", []byte("a\n"))
	writeChecksumFixture(t, root, "unrelated/large.bin", []byte("not part of the pointer set"))
	if err := os.Symlink("missing-target", filepath.Join(root, "unrelated-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	manifest, err := BuildPointerSHA256SUMS(root, "z.txt", "nested/a.txt")
	if err != nil {
		t.Fatalf("BuildPointerSHA256SUMS() error = %v", err)
	}
	want := checksumLine("nested/a.txt", []byte("a\n")) +
		checksumLine("z.txt", []byte("z\n"))
	if string(manifest) != want {
		t.Fatalf("BuildPointerSHA256SUMS() = %q, want %q", manifest, want)
	}
	if err := ValidatePointerSHA256SUMS(root, manifest, "nested/a.txt", "z.txt"); err != nil {
		t.Fatalf("ValidatePointerSHA256SUMS() error = %v", err)
	}

	writeChecksumFixture(t, root, "unrelated/another.bin", []byte("also ignored"))
	if err := ValidatePointerSHA256SUMS(root, manifest, "z.txt", "nested/a.txt"); err != nil {
		t.Fatalf("unrelated file changed sparse validation: %v", err)
	}
}

func TestValidatePointerSHA256SUMSRejectsManifestDrift(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "a.txt", []byte("a"))
	writeChecksumFixture(t, root, "b.txt", []byte("b"))
	a := checksumLine("a.txt", []byte("a"))
	b := checksumLine("b.txt", []byte("b"))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("a")))

	tests := []struct {
		name     string
		manifest string
		includes []string
		want     string
	}{
		{name: "missing entry", manifest: a, includes: []string{"a.txt", "b.txt"}, want: `missing included path "b.txt"`},
		{name: "extra entry", manifest: a + b, includes: []string{"a.txt"}, want: `extra path "b.txt"`},
		{name: "duplicate entry", manifest: a + a, includes: []string{"a.txt"}, want: `duplicate path "a.txt"`},
		{name: "unordered", manifest: b + a, includes: []string{"a.txt", "b.txt"}, want: "not in bytewise order"},
		{name: "noncanonical digest", manifest: strings.ToUpper(digest) + "  a.txt\n", includes: []string{"a.txt"}, want: "invalid lowercase SHA-256"},
		{name: "comment", manifest: "# pointer\n" + a, includes: []string{"a.txt"}, want: "invalid SHA256SUMS line"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertChecksumErrorContains(
				t,
				ValidatePointerSHA256SUMS(root, []byte(test.manifest), test.includes...),
				test.want,
			)
		})
	}

	manifest, err := BuildPointerSHA256SUMS(root, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	writeChecksumFixture(t, root, "a.txt", []byte("changed"))
	assertChecksumErrorContains(
		t,
		ValidatePointerSHA256SUMS(root, manifest, "a.txt"),
		`changed file "a.txt"`,
	)
}

func TestPointerSHA256SUMSRejectsUnsafeIncludes(t *testing.T) {
	root := t.TempDir()
	writeChecksumFixture(t, root, "file.txt", []byte("file"))
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		includes []string
		want     string
	}{
		{name: "empty set", want: "include set is empty"},
		{name: "duplicate", includes: []string{"file.txt", "file.txt"}, want: `duplicate pointer checksum include "file.txt"`},
		{name: "absolute", includes: []string{filepath.Join(root, "file.txt")}, want: "path must be relative"},
		{name: "dot segment", includes: []string{"nested/../file.txt"}, want: "canonical slash-relative path"},
		{name: "manifest itself", includes: []string{checksumManifestName}, want: "may not name SHA256SUMS"},
		{name: "directory", includes: []string{"directory"}, want: "not a regular file"},
		{name: "missing", includes: []string{"missing.txt"}, want: "no such file or directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildPointerSHA256SUMS(root, test.includes...)
			assertChecksumErrorContains(t, err, test.want)
		})
	}
}

func TestPointerSHA256SUMSRejectsMultiplyLinkedAndDuplicateFiles(t *testing.T) {
	t.Run("multiply linked", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "file.txt", []byte("file\n"))
		if err := os.Link(
			filepath.Join(root, "file.txt"),
			filepath.Join(t.TempDir(), "alias.txt"),
		); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		_, err := BuildPointerSHA256SUMS(root, "file.txt")
		assertChecksumErrorContains(
			t,
			err,
			"included file must have exactly one hard link",
		)
		assertChecksumErrorContains(
			t,
			ValidatePointerSHA256SUMS(
				root,
				[]byte(checksumLine("file.txt", []byte("file\n"))),
				"file.txt",
			),
			"included file must have exactly one hard link",
		)
		_, err = BuildPointerSHA256SUMS(root, "file.txt", "z-missing.txt")
		assertChecksumErrorContains(
			t,
			err,
			"included file must have exactly one hard link",
		)
	})

	t.Run("two multiply linked includes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "first.txt", []byte("file\n"))
		if err := os.Link(
			filepath.Join(root, "first.txt"),
			filepath.Join(root, "second.txt"),
		); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		_, err := BuildPointerSHA256SUMS(root, "first.txt", "second.txt")
		assertChecksumErrorContains(t, err, "must have exactly one hard link")
		manifest := checksumLine("first.txt", []byte("file\n")) +
			checksumLine("second.txt", []byte("file\n"))
		assertChecksumErrorContains(
			t,
			ValidatePointerSHA256SUMS(
				root,
				[]byte(manifest),
				"first.txt",
				"second.txt",
			),
			"must have exactly one hard link",
		)
	})
}

func TestChecksumFinalSnapshotRevalidationRejectsHybridContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	writeChecksumFixture(t, root, "file.txt", []byte("first"))
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sourceAuditFileHasOneLink(fileInfo) {
		t.Skip("platform does not expose an authenticated single-link count")
	}
	rootHandle, err := openPointerChecksumRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, fileInfo, err := digestChecksumFileAt(
		rootHandle,
		"file.txt",
		fileInfo,
		defaultChecksumLimits.maxFileBytes,
	)
	if closeErr := rootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]checksumPathSnapshot{
		"":         {info: rootInfo, directory: true},
		"file.txt": {info: fileInfo, digest: digest},
	}
	if err := os.WriteFile(path, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fileInfo.ModTime(), fileInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	exclusions, err := newChecksumExclusions(nil, defaultChecksumLimits)
	if err != nil {
		t.Fatal(err)
	}
	assertChecksumErrorContains(
		t,
		revalidateChecksumClosureSnapshot(root, snapshots, exclusions, defaultChecksumLimits),
		`checksum file "file.txt" changed after checksumming`,
	)
}

func TestPointerFinalSnapshotRevalidationRejectsHybridContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	writeChecksumFixture(t, root, "file.txt", []byte("first"))
	rootHandle, err := openPointerChecksumRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rootHandle.Close(); err != nil {
			t.Errorf("close checksum root: %v", err)
		}
	}()
	rootInfo, err := rootHandle.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	var fileInfo os.FileInfo
	digest, _, err := digestPointerChecksumFile(
		rootHandle,
		"file.txt",
		defaultChecksumLimits.maxFileBytes,
		defaultChecksumLimits.maxTotalBytes,
		defaultChecksumLimits.maxTotalBytes,
		&fileInfo,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sourceAuditFileHasOneLink(fileInfo) {
		t.Skip("platform does not expose an authenticated single-link count")
	}
	if err := os.WriteFile(path, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fileInfo.ModTime(), fileInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	assertChecksumErrorContains(
		t,
		revalidatePointerChecksumSnapshot(
			rootHandle,
			rootInfo,
			[]checksumEntry{{path: "file.txt", digest: digest, info: fileInfo}},
			defaultChecksumLimits,
		),
		`pointer checksum file "file.txt" changed after checksumming`,
	)
}

func TestPointerSHA256SUMSRejectsSymlinkTraversal(t *testing.T) {
	t.Run("filesystem root", func(t *testing.T) {
		filesystemRoot := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
		_, err := BuildPointerSHA256SUMS(filesystemRoot, "file.txt")
		assertChecksumErrorContains(t, err, "must not be a filesystem root")
	})
	t.Run("included symlink", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "target.txt", []byte("target"))
		if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildPointerSHA256SUMS(root, "link.txt")
		assertChecksumErrorContains(t, err, "included path is a symlink")
	})
	t.Run("symlink directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeChecksumFixture(t, outside, "file.txt", []byte("outside"))
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildPointerSHA256SUMS(root, "link/file.txt")
		assertChecksumErrorContains(t, err, "path traverses symlink directory")
	})
	t.Run("symlink root ancestor", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		writeChecksumFixture(t, target, "file.txt", []byte("file"))
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := BuildPointerSHA256SUMS(link, "file.txt")
		assertChecksumErrorContains(t, err, "root path must not traverse symlinks")
	})
}

func TestPointerSHA256SUMSResourceBounds(t *testing.T) {
	baseLimits := checksumLimits{
		maxEntries:       10,
		maxFiles:         10,
		maxPathBytes:     100,
		maxDepth:         10,
		maxFileBytes:     100,
		maxTotalBytes:    100,
		maxManifestBytes: 1_000,
	}

	t.Run("include count", func(t *testing.T) {
		limits := baseLimits
		limits.maxFiles = 1
		_, err := buildPointerSHA256SUMS(t.TempDir(), []string{"a", "b"}, limits)
		assertChecksumErrorContains(t, err, "include set exceeds 1 files")
	})
	t.Run("file bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "large", []byte("12345"))
		limits := baseLimits
		limits.maxFileBytes = 4
		_, err := buildPointerSHA256SUMS(root, []string{"large"}, limits)
		assertChecksumErrorContains(t, err, "file exceeds 4 bytes")
	})
	t.Run("total bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "a", []byte("123"))
		writeChecksumFixture(t, root, "b", []byte("456"))
		limits := baseLimits
		limits.maxTotalBytes = 5
		_, err := buildPointerSHA256SUMS(root, []string{"a", "b"}, limits)
		assertChecksumErrorContains(t, err, "files exceed 5 bytes")
	})
	t.Run("manifest bytes", func(t *testing.T) {
		root := t.TempDir()
		writeChecksumFixture(t, root, "a", []byte("a"))
		limits := baseLimits
		limits.maxManifestBytes = 1
		_, err := buildPointerSHA256SUMS(root, []string{"a"}, limits)
		assertChecksumErrorContains(t, err, "SHA256SUMS exceeds 1 bytes")
		assertChecksumErrorContains(
			t,
			validatePointerSHA256SUMS(root, []byte("xx"), []string{"a"}, limits),
			"SHA256SUMS exceeds 1 bytes",
		)
	})
	t.Run("manifest bytes fail before filesystem access", func(t *testing.T) {
		limits := baseLimits
		limits.maxManifestBytes = 1
		_, err := buildPointerSHA256SUMS(t.TempDir(), []string{"missing"}, limits)
		assertChecksumErrorContains(t, err, "SHA256SUMS exceeds 1 bytes")
	})
}

func checksumLine(path string, content []byte) string {
	digest := sha256.Sum256(content)
	return fmt.Sprintf("%x  %s\n", digest, path)
}

func writeChecksumFixture(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertChecksumErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
