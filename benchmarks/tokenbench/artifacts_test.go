package tokenbench

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	executionsnapshot "github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
)

type artifactBundleFixture struct {
	root     string
	raw      []byte
	digest   string
	manifest ArtifactManifest
	loaded   LoadedSuite
}

func TestLoadArtifactBundleBindsExactStaticRoleSet(t *testing.T) {
	fixture := newArtifactBundleFixture(t)
	useArtifactBuildPolicyForTest(t, fixture.digest)
	bundle, err := LoadArtifactBundle(fixture.root, fixture.loaded)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := bundle.reverify(fixture.loaded)
	if err != nil {
		t.Fatal(err)
	}
	if origins.Codex.Path != fixture.loaded.suite.HarnessExecutable ||
		origins.Codex.SHA256 != fixture.loaded.suite.HarnessSHA256 ||
		origins.Git.Path != fixture.loaded.suite.GitExecutable ||
		origins.Git.SHA256 != fixture.loaded.suite.GitExecutableSHA256 {
		t.Fatalf("suite/bundle cross-check failed: %+v", origins)
	}
	if bundle.state == nil || bundle.state.audit.ManifestSHA256 != fixture.digest ||
		!reflect.DeepEqual(bundle.state.audit.RawManifest, fixture.raw) ||
		!reflect.DeepEqual(bundle.state.audit.Provenance, fixture.manifest.Provenance) {
		t.Fatal("prepared bundle omitted exact manifest audit data")
	}
}

func TestDecodeArtifactManifestRejectsUnknownDuplicateAndTrailingData(t *testing.T) {
	fixture := newArtifactBundleFixture(t)
	raw := fixture.raw
	tests := map[string][]byte{
		"unknown": append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...),
		"duplicate": []byte(strings.Replace(
			string(raw),
			`"schema_version":"tokenbench.artifact-manifest/v2"`,
			`"schema_version":"tokenbench.artifact-manifest/v2","schema_version":"tokenbench.artifact-manifest/v2"`,
			1,
		)),
		"trailing whitespace": append(append([]byte(nil), raw...), '\n'),
		"trailing document":   append(append([]byte(nil), raw...), []byte(`{}`)...),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArtifactManifest(content); err == nil {
				t.Fatal("noncanonical artifact manifest was accepted")
			}
		})
	}
}

func TestArtifactManifestRejectsPathsDigestsAndProvenance(t *testing.T) {
	base := newArtifactBundleFixture(t).manifest
	tests := map[string]func(*ArtifactManifest){
		"absolute path": func(value *ArtifactManifest) { value.ScopeSifter.Path = "/escape" },
		"parent path":   func(value *ArtifactManifest) { value.ScopeSifter.Path = "../escape" },
		"nonclean path": func(value *ArtifactManifest) { value.ScopeSifter.Path = "bin/../scopesifter" },
		"shared path":   func(value *ArtifactManifest) { value.ScopeSifter.Path = value.Codex.Path },
		"bad digest":    func(value *ArtifactManifest) { value.ScopeSifter.SHA256 = "bad" },
		"shared digest": func(value *ArtifactManifest) { value.ScopeSifter.SHA256 = value.Codex.SHA256 },
		"source URI":    func(value *ArtifactManifest) { value.Provenance.SourceURI = "relative" },
		"source revision": func(value *ArtifactManifest) {
			value.Provenance.SourceRevision = "revision with space"
		},
		"recipe":  func(value *ArtifactManifest) { value.Provenance.RecipeSHA256 = "bad" },
		"builder": func(value *ArtifactManifest) { value.Provenance.BuilderImageDigest = "latest" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := base
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("invalid artifact manifest was accepted")
			}
		})
	}
}

func TestArtifactBuildPolicyFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		policy func(artifactBundleFixture) string
		want   string
	}{
		{"blank", func(artifactBundleFixture) string { return "" }, "no trusted"},
		{"malformed", func(artifactBundleFixture) string { return "not-a-digest" }, "malformed"},
		{"mismatch", func(artifactBundleFixture) string { return SHA256([]byte("other")) }, "not allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newArtifactBundleFixture(t)
			useArtifactBuildPolicyForTest(t, test.policy(fixture))
			if _, err := LoadArtifactBundle(fixture.root, fixture.loaded); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadArtifactBundle() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadArtifactBundleCrossChecksSuiteCodexAndGit(t *testing.T) {
	tests := map[string]func(*Suite){
		"Codex path":   func(value *Suite) { value.HarnessExecutable += "-other" },
		"Codex digest": func(value *Suite) { value.HarnessSHA256 = SHA256([]byte("other-codex")) },
		"Git path":     func(value *Suite) { value.GitExecutable += "-other" },
		"Git digest":   func(value *Suite) { value.GitExecutableSHA256 = SHA256([]byte("other-git")) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newArtifactBundleFixture(t)
			mutate(&fixture.loaded.suite)
			fixture.loaded = bindFixtureSuiteJSON(t, fixture.loaded)
			useArtifactBuildPolicyForTest(t, fixture.digest)
			if _, err := LoadArtifactBundle(fixture.root, fixture.loaded); err == nil ||
				!strings.Contains(err.Error(), "suite") {
				t.Fatalf("suite mismatch accepted: %v", err)
			}
		})
	}
}

func TestArtifactBundleRejectsSymlinkAndPostLoadDrift(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		fixture := newArtifactBundleFixture(t)
		rolePath := filepath.Join(fixture.root, filepath.FromSlash(fixture.manifest.ScopeSifter.Path))
		if err := os.Remove(rolePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(fixture.root, filepath.FromSlash(fixture.manifest.Codex.Path)),
			rolePath,
		); err != nil {
			t.Fatal(err)
		}
		useArtifactBuildPolicyForTest(t, fixture.digest)
		if _, err := LoadArtifactBundle(fixture.root, fixture.loaded); err == nil {
			t.Fatal("symlinked artifact was accepted")
		}
	})

	t.Run("manifest drift", func(t *testing.T) {
		fixture := newArtifactBundleFixture(t)
		useArtifactBuildPolicyForTest(t, fixture.digest)
		bundle, err := LoadArtifactBundle(fixture.root, fixture.loaded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(fixture.root, ArtifactManifestFilename),
			[]byte(`{}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := bundle.reverify(fixture.loaded); err == nil {
			t.Fatal("post-load manifest drift was accepted")
		}
	})

	t.Run("file drift", func(t *testing.T) {
		fixture := newArtifactBundleFixture(t)
		useArtifactBuildPolicyForTest(t, fixture.digest)
		bundle, err := LoadArtifactBundle(fixture.root, fixture.loaded)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.root, filepath.FromSlash(fixture.manifest.ScopeSifter.Path))
		if err := os.WriteFile(path, minimalStaticELF("changed"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := bundle.reverify(fixture.loaded); err == nil {
			t.Fatal("post-load artifact drift was accepted")
		}
	})
}

func TestArtifactVerificationRejectsDynamicELF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dynamic")
	if err := os.WriteFile(path, minimalDynamicELF(), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, verifyErr := identifyStableStaticELF(file)
	closeErr := file.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if verifyErr == nil || !strings.Contains(verifyErr.Error(), "dynamic interpreter") {
		t.Fatalf("dynamic ELF verification = %v", verifyErr)
	}
}

func TestArtifactRoleSwapBreaksPolicyAndOriginBinding(t *testing.T) {
	fixture := newArtifactBundleFixture(t)
	useArtifactBuildPolicyForTest(t, fixture.digest)
	bundle, err := LoadArtifactBundle(fixture.root, fixture.loaded)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := bundle.reverify(fixture.loaded)
	if err != nil {
		t.Fatal(err)
	}
	originInputs := originInputsForArtifacts(t, origins)
	if err := bundle.state.audit.validateBinding(originInputs); err != nil {
		t.Fatalf("valid artifact/origin binding: %v", err)
	}

	swapped := fixture.manifest
	swapped.Utilities.Sed, swapped.Utilities.Awk = swapped.Utilities.Awk, swapped.Utilities.Sed
	swappedRaw, err := json.Marshal(swapped)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, ArtifactManifestFilename), swappedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadArtifactBundle(fixture.root, fixture.loaded); err == nil {
		t.Fatal("role-swapped manifest bypassed suite/build policy")
	}

	audit := cloneArtifactBundleAudit(bundle.state.audit)
	audit.Manifest = swapped
	audit.RawManifest = swappedRaw
	audit.ManifestSHA256 = SHA256(swappedRaw)
	audit.Provenance = swapped.Provenance
	if err := audit.validateBinding(originInputs); err == nil ||
		!strings.Contains(err.Error(), "role") {
		t.Fatalf("role-swapped audit binding = %v", err)
	}
}

func TestArtifactBundleAuditRejectsRawAndProvenanceDrift(t *testing.T) {
	fixture := newArtifactBundleFixture(t)
	audit := ArtifactBundleAudit{
		SchemaVersion: ArtifactBundleAuditVersion,
		Root:          fixture.root, Manifest: fixture.manifest,
		RawManifest: append([]byte(nil), fixture.raw...), ManifestSHA256: fixture.digest,
		Provenance: fixture.manifest.Provenance,
	}
	if err := audit.Validate(); err != nil {
		t.Fatal(err)
	}
	rawDrift := cloneArtifactBundleAudit(audit)
	rawDrift.RawManifest = append(rawDrift.RawManifest, '\n')
	if err := rawDrift.Validate(); err == nil {
		t.Fatal("artifact audit accepted raw-manifest drift")
	}
	provenanceDrift := cloneArtifactBundleAudit(audit)
	provenanceDrift.Provenance.SourceRevision = "different"
	if err := provenanceDrift.Validate(); err == nil {
		t.Fatal("artifact audit accepted provenance drift")
	}
}

func TestPrepareOriginsRejectsUnloadedArtifactCapability(t *testing.T) {
	if _, err := PrepareOrigins(context.Background(), validLoadedSuite(), preparedArtifactBundle{}); err == nil ||
		!strings.Contains(err.Error(), "LoadArtifactBundle") {
		t.Fatalf("PrepareOrigins(zero bundle) = %v", err)
	}
}

func newArtifactBundleFixture(t *testing.T) artifactBundleFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	next := func(name string) ArtifactFile {
		content := minimalStaticELF(name)
		path := filepath.Join(root, "bin", name)
		if err := os.WriteFile(path, content, 0o700); err != nil {
			t.Fatal(err)
		}
		return ArtifactFile{Path: filepath.ToSlash(filepath.Join("bin", name)), SHA256: SHA256(content)}
	}
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		Provenance: ArtifactProvenance{
			SourceURI:      "https://github.com/example/tokenbench-artifacts",
			SourceRevision: strings.Repeat("a", 40), RecipeSHA256: SHA256([]byte("recipe")),
			BuilderImageDigest: "sha256:" + SHA256([]byte("builder-image")),
		},
		Codex: next("codex"), ScopeSifter: next("scopesifter"),
		StaticGit: next("git"), StaticBash: next("bash"),
		Utilities: ArtifactUtilities{
			Ripgrep: next("rg"), Sed: next("sed"), Awk: next("awk"),
			Find: next("find"), Head: next("head"), Tail: next("tail"),
			WC: next("wc"), Sort: next("sort"), Cut: next("cut"),
			Tr: next("tr"), Cat: next("cat"), LS: next("ls"),
			Grep: next("grep"), Xargs: next("xargs"),
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digestValue := SHA256(raw)
	if err := os.WriteFile(filepath.Join(root, ArtifactManifestFilename), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := validLoadedSuite()
	loaded.suite.HarnessExecutable = filepath.Join(root, filepath.FromSlash(manifest.Codex.Path))
	loaded.suite.HarnessSHA256 = manifest.Codex.SHA256
	loaded.suite.GitExecutable = filepath.Join(root, filepath.FromSlash(manifest.StaticGit.Path))
	loaded.suite.GitExecutableSHA256 = manifest.StaticGit.SHA256
	loaded.suite.ArtifactManifestSHA256 = digestValue
	loaded = bindFixtureSuiteJSON(t, loaded)
	return artifactBundleFixture{
		root: root, raw: raw, digest: digestValue, manifest: manifest, loaded: loaded,
	}
}

func minimalStaticELF(tag string) []byte {
	content := make([]byte, 64, 64+len(tag))
	copy(content[:4], []byte{0x7f, 'E', 'L', 'F'})
	content[4] = 2                                    // ELFCLASS64
	content[5] = 1                                    // ELFDATA2LSB
	content[6] = 1                                    // EV_CURRENT
	binary.LittleEndian.PutUint16(content[16:18], 2)  // ET_EXEC
	binary.LittleEndian.PutUint16(content[18:20], 62) // EM_X86_64
	binary.LittleEndian.PutUint32(content[20:24], 1)  // EV_CURRENT
	binary.LittleEndian.PutUint16(content[52:54], 64)
	binary.LittleEndian.PutUint16(content[54:56], 56)
	binary.LittleEndian.PutUint16(content[58:60], 64)
	return append(content, tag...)
}

func minimalDynamicELF() []byte {
	interpreter := []byte("/loader\x00")
	content := make([]byte, 64+56+len(interpreter))
	copy(content, minimalStaticELF(""))
	binary.LittleEndian.PutUint64(content[32:40], 64)
	binary.LittleEndian.PutUint16(content[56:58], 1)
	program := content[64 : 64+56]
	binary.LittleEndian.PutUint32(program[0:4], 3) // PT_INTERP
	binary.LittleEndian.PutUint64(program[8:16], uint64(64+56))
	binary.LittleEndian.PutUint64(program[32:40], uint64(len(interpreter)))
	binary.LittleEndian.PutUint64(program[40:48], uint64(len(interpreter)))
	copy(content[64+56:], interpreter)
	return content
}

func originInputsForArtifacts(t *testing.T, artifacts artifactOriginSet) executionsnapshot.OriginInputs {
	t.Helper()
	origins, err := executionsnapshot.NewOriginInputs(
		executionsnapshot.SourceOrigin{
			Root: "/source", Revision: strings.Repeat("a", 40), Base: strings.Repeat("b", 40),
			TreeSHA256: SHA256([]byte("tree")), GitMetadataSHA256: SHA256([]byte("git-metadata")),
		},
		artifacts.Codex, artifacts.ScopeSifter, artifacts.Git, artifacts.Bash,
		artifacts.Utilities,
		executionsnapshot.FileOrigin{Path: "/runner", SHA256: SHA256([]byte("runner"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return origins
}
