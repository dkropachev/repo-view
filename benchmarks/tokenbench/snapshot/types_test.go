package snapshot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestOriginInputsRejectsSharedExecutableRole(t *testing.T) {
	origins := originFixture(t)
	origins.Utilities.Cat = origins.Utilities.Sed
	origins.Commitment = mustOriginCommitment(t, origins)
	if err := origins.Validate(); err == nil || !strings.Contains(err.Error(), "share an origin") {
		t.Fatalf("Validate() = %v, want shared-role rejection", err)
	}
}

func TestOriginInputsRejectsSharedExecutableDigestAtDistinctPaths(t *testing.T) {
	origins := originFixture(t)
	origins.Utilities.Cat.SHA256 = origins.Utilities.Sed.SHA256
	origins.Commitment = mustOriginCommitment(t, origins)
	if err := origins.Validate(); err == nil || !strings.Contains(err.Error(), "image digest") {
		t.Fatalf("Validate() = %v, want shared-image rejection", err)
	}
}

func TestExecutionInputsPureValidationRejectsExecutableSurfaceAndMountForgeries(t *testing.T) {
	base := executionFixture(t)
	if err := base.Validate(); err != nil {
		t.Fatalf("fixture Validate(): %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ExecutionInputs)
		want   string
	}{
		{
			name: "verifier Git enters arm",
			mutate: func(value *ExecutionInputs) {
				value.ExecutablePaths = append(value.ExecutablePaths, value.VerifierGitExecutable)
				sort.Strings(value.ExecutablePaths)
			},
			want: "executable policy",
		},
		{
			name: "utility becomes dynamic",
			mutate: func(value *ExecutionInputs) {
				for index := range value.Manifest {
					if value.Manifest[index].SnapshotPath == value.Utilities.Ripgrep {
						value.Manifest[index].ELF.Interpreter = "/lib64/ld-linux-x86-64.so.2"
						value.Manifest[index].ELF.LoaderSHA256 = strings.Repeat("a", 64)
						value.Manifest[index].ELF.Static = false
					}
				}
				value.ManifestSHA256, _ = manifestSHA256(value.Manifest)
			},
			want: "static image",
		},
		{
			name: "mount writable",
			mutate: func(value *ExecutionInputs) {
				value.PathIsolation.ReadOnly = false
				value.PathIsolation.Commitment, _ = mountCommitment(value.PathIsolation)
			},
			want: "read-only",
		},
		{
			name: "cache path forged",
			mutate: func(value *ExecutionInputs) {
				value.ChangedState.Path = filepath.Join(value.SnapshotRoot, "source", "cache.json")
			},
			want: "cache path",
		},
		{
			name: "cache patch forged",
			mutate: func(value *ExecutionInputs) {
				value.ChangedStateCache.Patch += "forged"
			},
			want: "embedded changed-state cache digest",
		},
		{
			name: "per-file cache patch forged",
			mutate: func(value *ExecutionInputs) {
				value.ChangedStateCache.ChangedFiles[0].Patch += "forged"
			},
			want: "per-file patch",
		},
		{
			name: "parent mount becomes shared",
			mutate: func(value *ExecutionInputs) {
				value.PathIsolation.ParentOptionalFields = []string{"shared:7"}
				value.PathIsolation.Commitment, _ = mountCommitment(value.PathIsolation)
			},
			want: "parent propagation",
		},
		{
			name: "snapshot mount becomes shared",
			mutate: func(value *ExecutionInputs) {
				value.PathIsolation.OptionalFields = []string{"shared:8"}
				value.PathIsolation.Commitment, _ = mountCommitment(value.PathIsolation)
			},
			want: "snapshot mount propagation",
		},
		{
			name: "verifier Git becomes dynamic",
			mutate: func(value *ExecutionInputs) {
				for index := range value.Manifest {
					if value.Manifest[index].SnapshotPath == value.VerifierGitExecutable {
						value.Manifest[index].ELF.Interpreter = "/lib64/ld-linux-x86-64.so.2"
						value.Manifest[index].ELF.LoaderSHA256 = strings.Repeat("a", 64)
						value.Manifest[index].ELF.Static = false
					}
				}
				value.ManifestSHA256, _ = manifestSHA256(value.Manifest)
			},
			want: "static image",
		},
		{
			name: "utility ABI differs from runner",
			mutate: func(value *ExecutionInputs) {
				for index := range value.Manifest {
					if value.Manifest[index].SnapshotPath == value.Utilities.Ripgrep {
						value.Manifest[index].ELF.Machine = "EM_AARCH64"
					}
				}
				value.ManifestSHA256, _ = manifestSHA256(value.Manifest)
			},
			want: "ELF ABI differs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneInputs(base)
			test.mutate(&value)
			value.Commitment = mustExecutionCommitment(t, value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExecutionInputsDefensiveClone(t *testing.T) {
	base := executionFixture(t)
	clone := cloneInputs(base)
	clone.Manifest[0].LogicalOrigin = "mutated"
	clone.PathIsolation.MountOptions[0] = "mutated"
	clone.ChangedStateCache.ChangedFiles[0].Lines[0].Start = 99
	if reflect.DeepEqual(base, clone) || base.Manifest[0].LogicalOrigin == "mutated" ||
		base.PathIsolation.MountOptions[0] == "mutated" ||
		base.ChangedStateCache.ChangedFiles[0].Lines[0].Start == 99 {
		t.Fatal("cloneInputs shares mutable storage")
	}
}

func TestBindFilesystemRootAccountsForParentSubtreeMount(t *testing.T) {
	root, err := bindFilesystemRoot("/filesystem/subtree", "/mnt", "/mnt/run/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if root != "/filesystem/subtree/run/snapshot" {
		t.Fatalf("bindFilesystemRoot() = %q", root)
	}
	root, err = bindFilesystemRoot("/", "/", "/run/snapshot")
	if err != nil || root != "/run/snapshot" {
		t.Fatalf("bindFilesystemRoot(root parent) = %q, %v", root, err)
	}
}

func TestChangedStateRejectsUnmergedSpansAndUnsafePath(t *testing.T) {
	cache := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    strings.Repeat("a", 40),
		HeadCommit:    strings.Repeat("b", 40),
		HeadSubject:   "subject",
		ChangedFiles: []ChangedFileState{{
			Path: "a.go", Status: "modified",
			Lines: []ChangedLineSpan{{Start: 1, End: 2}, {Start: 3, End: 4}},
			Patch: "patch\n", PatchSHA256: digest([]byte("patch\n")),
		}},
		Patch: "patch\n",
	}
	if err := cache.Validate(); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Fatalf("Validate() = %v, want unmerged-span rejection", err)
	}
	cache.ChangedFiles[0].Lines = []ChangedLineSpan{{Start: 1, End: 2}}
	cache.ChangedFiles[0].Path = "../escape"
	if err := cache.Validate(); err == nil || !strings.Contains(err.Error(), "strictly sorted") {
		t.Fatalf("Validate() = %v, want unsafe-path rejection", err)
	}
}

func TestChangedStateBoundsExpandedLineCount(t *testing.T) {
	cache := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    strings.Repeat("a", 40), HeadCommit: strings.Repeat("b", 40),
		HeadSubject: "subject",
		ChangedFiles: []ChangedFileState{{
			Path: "a.go", Status: "modified",
			Lines: []ChangedLineSpan{{Start: 1, End: maximumExpandedChangedLines}},
			Patch: "patch\n", PatchSHA256: digest([]byte("patch\n")),
		}},
		Patch: "patch\n",
	}
	if err := cache.Validate(); err != nil {
		t.Fatalf("Validate() at expanded-line bound: %v", err)
	}
	cache.ChangedFiles[0].Lines[0].End++
	if err := cache.Validate(); err == nil || !strings.Contains(err.Error(), "expanded line count") {
		t.Fatalf("Validate() above expanded-line bound = %v", err)
	}
}

func originFixture(t *testing.T) OriginInputs {
	t.Helper()
	next := 0
	file := func(name string) FileOrigin {
		next++
		return FileOrigin{
			Path:   filepath.Join("/origins", name),
			SHA256: fmt.Sprintf("%064x", next),
		}
	}
	origins, err := NewOriginInputs(
		SourceOrigin{
			Root: "/origin/source", Revision: strings.Repeat("a", 40),
			Base: strings.Repeat("b", 40), TreeSHA256: strings.Repeat("c", 64),
			GitMetadataSHA256: strings.Repeat("d", 64),
		},
		file("codex"), file("repo-view"), file("git"), file("bash"),
		UtilityOrigins{
			Ripgrep: file("rg"), Sed: file("sed"), Awk: file("awk"),
			Find: file("find"), Head: file("head"), Tail: file("tail"),
			WC: file("wc"), Sort: file("sort"), Cut: file("cut"), Tr: file("tr"),
			Cat: file("cat"), LS: file("ls"), Grep: file("grep"), Xargs: file("xargs"),
		},
		file("runner"),
	)
	if err != nil {
		t.Fatalf("NewOriginInputs(): %v", err)
	}
	return origins
}

func executionFixture(t *testing.T) ExecutionInputs {
	t.Helper()
	root := "/snapshot"
	origins := originFixture(t)
	utilities := utilityPathsForRoot(filepath.Join(root, "toolbox"))
	cache := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    origins.Source.Base,
		HeadCommit:    origins.Source.Revision,
		HeadSubject:   "subject",
		ChangedFiles: []ChangedFileState{{
			Path: "a.go", Status: "modified", Lines: []ChangedLineSpan{{Start: 2, End: 3}},
			Patch: "diff --git a/a.go b/a.go\n", PatchSHA256: digest([]byte("diff --git a/a.go b/a.go\n")),
		}},
		Patch: "diff --git a/a.go b/a.go\n",
	}
	cacheRaw, _ := json.Marshal(cache)
	staticELF := func() *ELFIdentity {
		return &ELFIdentity{
			Class: "ELFCLASS64", Data: "ELFDATA2LSB", Machine: "EM_X86_64",
			Type: "ET_DYN", Needed: []string{}, Static: true,
		}
	}
	directoryPaths := []string{
		root,
		filepath.Join(root, "source"),
		filepath.Join(root, "source", ".git"),
		filepath.Join(root, "tools"),
		filepath.Join(root, "toolbox"),
		filepath.Join(root, "cache"),
	}
	manifest := make([]ManifestEntry, 0)
	for _, path := range directoryPaths {
		logical, _ := logicalOriginForPath(root, path)
		manifest = append(manifest, ManifestEntry{
			LogicalOrigin: logical, SnapshotPath: path, Kind: ManifestKindDirectory,
			Mode: 0o500, SHA256: emptySHA256,
		})
	}
	filePaths := append([]string{
		filepath.Join(root, "tools", "codex"),
		filepath.Join(root, "tools", "repo-view"),
		filepath.Join(root, "tools", "verifier-git"),
		filepath.Join(root, "tools", "runner-arm-init"),
		filepath.Join(root, "toolbox", "bash"),
	}, utilities.values()...)
	for _, path := range filePaths {
		logical, _ := logicalOriginForPath(root, path)
		manifest = append(manifest, ManifestEntry{
			LogicalOrigin: logical, SnapshotPath: path, Kind: ManifestKindFile,
			Mode: 0o555, Size: 1, SHA256: strings.Repeat("e", 64), FSVerity: true,
			FSVerityAlgorithm:   FSVerityAlgorithm,
			FSVerityMeasurement: strings.Repeat("f", 64), ELF: staticELF(),
		})
	}
	cachePath := filepath.Join(root, "cache", "changed-state.json")
	cacheLogical, _ := logicalOriginForPath(root, cachePath)
	manifest = append(manifest, ManifestEntry{
		LogicalOrigin: cacheLogical, SnapshotPath: cachePath, Kind: ManifestKindFile,
		Mode: 0o444, Size: int64(len(cacheRaw)), SHA256: digest(cacheRaw), FSVerity: true,
		FSVerityAlgorithm:   FSVerityAlgorithm,
		FSVerityMeasurement: strings.Repeat("1", 64),
	})
	sort.Slice(manifest, func(left, right int) bool {
		return manifest[left].LogicalOrigin < manifest[right].LogicalOrigin
	})
	mount := MountIdentity{
		SchemaVersion: MountSchemaVersion, MountID: 10, ParentID: 9,
		MountNamespaceDevice: 3, MountNamespaceInode: 4,
		ParentMountPoint: "/", ParentFilesystemRoot: "/", ParentOptionalFields: []string{},
		MajorMinor: "8:1", FilesystemRoot: root, MountPoint: root,
		MountOptions: []string{"nodev", "nosuid", "ro"}, OptionalFields: []string{},
		FilesystemType: "ext4", Source: "/dev/test", SuperOptions: []string{"rw"},
		Device: 1, Inode: 2, ReadOnly: true, NoSUID: true, NoDev: true, SelfBind: true,
	}
	mount.Commitment, _ = mountCommitment(mount)
	inputs := ExecutionInputs{
		SchemaVersion: ExecutionSchemaVersion, SnapshotRoot: root,
		SourceRoot:            filepath.Join(root, "source"),
		GitMetadataRoot:       filepath.Join(root, "source", ".git"),
		CodexExecutable:       filepath.Join(root, "tools", "codex"),
		RepoViewExecutable:    filepath.Join(root, "tools", "repo-view"),
		VerifierGitExecutable: filepath.Join(root, "tools", "verifier-git"),
		BashExecutable:        filepath.Join(root, "toolbox", "bash"),
		Utilities:             utilities, ToolboxRoot: filepath.Join(root, "toolbox"),
		RunnerExecutable:       filepath.Join(root, "tools", "runner-arm-init"),
		ArmInitExecutable:      filepath.Join(root, "tools", "runner-arm-init"),
		RunnerArmInitSameImage: true,
		SourceRevision:         origins.Source.Revision, SourceBaseRevision: origins.Source.Base,
		SourceTreeSHA256:  origins.Source.TreeSHA256,
		GitMetadataSHA256: origins.Source.GitMetadataSHA256,
		OriginCommitment:  origins.Commitment,
		ChangedState: ChangedStateIdentity{
			SchemaVersion: ChangedStateSchemaVersion, Path: cachePath, SHA256: digest(cacheRaw),
			BaseCommit: cache.BaseCommit, HeadCommit: cache.HeadCommit,
			HeadSubjectSHA256: digest([]byte(cache.HeadSubject)),
			ChangedFileCount:  len(cache.ChangedFiles), PatchBytes: len(cache.Patch),
			PerFilePatchBytes: changedStatePerFilePatchBytes(cache),
		},
		ChangedStateCache: cache, PathIsolation: mount, Manifest: manifest,
		ReadOnlyPaths: []string{filepath.Join(root, "source"), cachePath},
		ExecutablePaths: append([]string{
			filepath.Join(root, "tools", "codex"),
			filepath.Join(root, "tools", "repo-view"),
			filepath.Join(root, "toolbox", "bash"),
		}, utilities.values()...),
	}
	sort.Strings(inputs.ExecutablePaths)
	inputs.ManifestSHA256, _ = manifestSHA256(inputs.Manifest)
	inputs.Commitment = mustExecutionCommitment(t, inputs)
	return inputs
}

func mustOriginCommitment(t *testing.T, value OriginInputs) string {
	t.Helper()
	commitment, err := originCommitment(value)
	if err != nil {
		t.Fatal(err)
	}
	return commitment
}

func mustExecutionCommitment(t *testing.T, value ExecutionInputs) string {
	t.Helper()
	commitment, err := executionCommitment(value)
	if err != nil {
		t.Fatal(err)
	}
	return commitment
}
