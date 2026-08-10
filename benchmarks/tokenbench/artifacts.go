package tokenbench

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	executionsnapshot "github.com/dkropachev/repo-view/benchmarks/tokenbench/snapshot"
)

const (
	// ArtifactManifestSchemaVersion is the canonical authored execution bundle
	// schema. The JSON encoding must exactly equal json.Marshal of this model.
	ArtifactManifestSchemaVersion = "tokenbench.artifact-manifest/v1"
	ArtifactBundleAuditVersion    = "tokenbench.artifact-bundle-audit/v1"
	ArtifactManifestFilename      = "tokenbench-artifacts-v1.json"

	maximumArtifactManifestBytes = 64 << 10
	maximumArtifactFileBytes     = int64(256 << 20)
	maximumArtifactBundleBytes   = int64(2 << 30)
	maximumArtifactURIBytes      = 2_048
	maximumArtifactRevisionBytes = 256
)

// trustedArtifactManifestSHA256 is set only at link time, for example:
//
//	-X github.com/dkropachev/repo-view/benchmarks/tokenbench.trustedArtifactManifestSHA256=<sha256>
//
// A blank or malformed value disables publishable artifact preparation. It is
// intentionally unexported: runtime callers cannot widen the binary's policy.
var trustedArtifactManifestSHA256 string

// ArtifactFile binds one closed role to a canonical bundle-relative pathname
// and exact bytes.
type ArtifactFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ArtifactProvenance is bounded build provenance for the entire closed bundle.
type ArtifactProvenance struct {
	SourceURI          string `json:"source_uri"`
	SourceRevision     string `json:"source_revision"`
	RecipeSHA256       string `json:"recipe_sha256"`
	BuilderImageDigest string `json:"builder_image_digest"`
}

// ArtifactUtilities is the exact utility role surface. A map is deliberately
// not used: unknown or missing roles cannot be represented canonically.
type ArtifactUtilities struct {
	Ripgrep ArtifactFile `json:"rg"`
	Sed     ArtifactFile `json:"sed"`
	Awk     ArtifactFile `json:"awk"`
	Find    ArtifactFile `json:"find"`
	Head    ArtifactFile `json:"head"`
	Tail    ArtifactFile `json:"tail"`
	WC      ArtifactFile `json:"wc"`
	Sort    ArtifactFile `json:"sort"`
	Cut     ArtifactFile `json:"cut"`
	Tr      ArtifactFile `json:"tr"`
	Cat     ArtifactFile `json:"cat"`
	LS      ArtifactFile `json:"ls"`
	Grep    ArtifactFile `json:"grep"`
	Xargs   ArtifactFile `json:"xargs"`
}

// ArtifactManifest is the canonical strict v1 execution-artifact manifest.
// Every executable must be a distinct static ELF image.
type ArtifactManifest struct {
	SchemaVersion string             `json:"schema_version"`
	Provenance    ArtifactProvenance `json:"provenance"`
	Codex         ArtifactFile       `json:"codex"`
	RepoView      ArtifactFile       `json:"repo_view"`
	StaticGit     ArtifactFile       `json:"static_git"`
	StaticBash    ArtifactFile       `json:"static_bash"`
	Utilities     ArtifactUtilities  `json:"utilities"`
}

// ArtifactBundleAudit is serialized into publishable v3 plans. RawManifest is
// the exact authored byte sequence; Manifest and Provenance make review
// structured without weakening the byte commitment.
type ArtifactBundleAudit struct {
	Manifest       ArtifactManifest   `json:"manifest"`
	Provenance     ArtifactProvenance `json:"provenance"`
	SchemaVersion  string             `json:"schema_version"`
	Root           string             `json:"root"`
	ManifestSHA256 string             `json:"manifest_sha256"`
	RawManifest    []byte             `json:"raw_manifest"`
}

type artifactRole struct {
	name string
	file ArtifactFile
}

func (manifest ArtifactManifest) roles() []artifactRole {
	return []artifactRole{
		{"codex", manifest.Codex},
		{"repo-view", manifest.RepoView},
		{"static Git", manifest.StaticGit},
		{"static Bash", manifest.StaticBash},
		{"rg", manifest.Utilities.Ripgrep},
		{"sed", manifest.Utilities.Sed},
		{"awk", manifest.Utilities.Awk},
		{"find", manifest.Utilities.Find},
		{"head", manifest.Utilities.Head},
		{"tail", manifest.Utilities.Tail},
		{"wc", manifest.Utilities.WC},
		{"sort", manifest.Utilities.Sort},
		{"cut", manifest.Utilities.Cut},
		{"tr", manifest.Utilities.Tr},
		{"cat", manifest.Utilities.Cat},
		{"ls", manifest.Utilities.LS},
		{"grep", manifest.Utilities.Grep},
		{"xargs", manifest.Utilities.Xargs},
	}
}

// Validate checks the closed role surface, canonical relative paths, distinct
// path and byte identities, and bounded structured provenance.
func (manifest ArtifactManifest) Validate() error {
	if manifest.SchemaVersion != ArtifactManifestSchemaVersion {
		return fmt.Errorf("unexpected artifact manifest schema %q", manifest.SchemaVersion)
	}
	if err := manifest.Provenance.validate(); err != nil {
		return err
	}
	paths := make(map[string]string, len(manifest.roles()))
	digests := make(map[string]string, len(manifest.roles()))
	for _, role := range manifest.roles() {
		if !validArtifactRelativePath(role.file.Path) {
			return fmt.Errorf("artifact %s path is not canonical and bundle-relative", role.name)
		}
		if !ValidSHA256(role.file.SHA256) {
			return fmt.Errorf("artifact %s digest is invalid", role.name)
		}
		if previous, exists := paths[role.file.Path]; exists {
			return fmt.Errorf("artifact roles %s and %s share a path", previous, role.name)
		}
		if previous, exists := digests[role.file.SHA256]; exists {
			return fmt.Errorf("artifact roles %s and %s share an image digest", previous, role.name)
		}
		paths[role.file.Path] = role.name
		digests[role.file.SHA256] = role.name
	}
	return nil
}

func (provenance ArtifactProvenance) validate() error {
	if len(provenance.SourceURI) == 0 || len(provenance.SourceURI) > maximumArtifactURIBytes ||
		!utf8.ValidString(provenance.SourceURI) || strings.TrimSpace(provenance.SourceURI) != provenance.SourceURI ||
		strings.ContainsRune(provenance.SourceURI, '\x00') {
		return errors.New("artifact provenance source URI is invalid")
	}
	parsed, err := url.ParseRequestURI(provenance.SourceURI)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("artifact provenance source URI must be absolute and credential-free")
	}
	if len(provenance.SourceRevision) == 0 ||
		len(provenance.SourceRevision) > maximumArtifactRevisionBytes ||
		!utf8.ValidString(provenance.SourceRevision) ||
		strings.TrimSpace(provenance.SourceRevision) != provenance.SourceRevision ||
		strings.ContainsAny(provenance.SourceRevision, "\x00\r\n\t ") {
		return errors.New("artifact provenance source revision is invalid")
	}
	if !ValidSHA256(provenance.RecipeSHA256) {
		return errors.New("artifact provenance recipe digest is invalid")
	}
	builderDigest, found := strings.CutPrefix(provenance.BuilderImageDigest, "sha256:")
	if !found || !ValidSHA256(builderDigest) {
		return errors.New("artifact provenance builder image digest is invalid")
	}
	return nil
}

func validArtifactRelativePath(value string) bool {
	if value == "" || len(value) > maximumSuitePathBytes || !utf8.ValidString(value) ||
		strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') ||
		filepath.IsAbs(filepath.FromSlash(value)) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../")
}

func decodeArtifactManifest(raw []byte) (ArtifactManifest, error) {
	if len(raw) == 0 || len(raw) > maximumArtifactManifestBytes {
		return ArtifactManifest{}, fmt.Errorf(
			"artifact manifest must contain 1..%d bytes",
			maximumArtifactManifestBytes,
		)
	}
	if !utf8.Valid(raw) {
		return ArtifactManifest{}, errors.New("artifact manifest must be valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return ArtifactManifest{}, fmt.Errorf("decode artifact manifest: %w", err)
	}
	var manifest ArtifactManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ArtifactManifest{}, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ArtifactManifest{}, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ArtifactManifest{}, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return ArtifactManifest{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return ArtifactManifest{}, errors.New("artifact manifest JSON is not the exact canonical encoding")
	}
	return manifest, nil
}

type preparedArtifactBundleState struct {
	rootInfo    os.FileInfo
	suiteSHA256 string
	audit       ArtifactBundleAudit
}

// preparedArtifactBundle is an unforgeable loader capability. Although an
// external caller can pass the value returned by LoadArtifactBundle directly
// to PrepareOrigins, its type and state cannot be constructed outside this
// package.
type preparedArtifactBundle struct {
	state *preparedArtifactBundleState
}

type artifactOriginSet struct {
	Codex     executionsnapshot.FileOrigin
	RepoView  executionsnapshot.FileOrigin
	Git       executionsnapshot.FileOrigin
	Bash      executionsnapshot.FileOrigin
	Utilities executionsnapshot.UtilityOrigins
}

// LoadArtifactBundle strictly loads the code-owned manifest filename below an
// explicit root, enforces the suite and binary allowlist commitments, and
// verifies every closed executable role through traversal-safe file opens.
func LoadArtifactBundle(root string, loaded LoadedSuite) (preparedArtifactBundle, error) {
	if err := validateLoadedForPreparation(loaded); err != nil {
		return preparedArtifactBundle{}, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" ||
		len(root) > maximumSuitePathBytes {
		return preparedArtifactBundle{}, errors.New("artifact bundle root must be absolute, canonical, and non-root")
	}
	policyDigest, err := artifactBuildPolicyDigest()
	if err != nil {
		return preparedArtifactBundle{}, err
	}
	if loaded.suite.ArtifactManifestSHA256 != policyDigest {
		return preparedArtifactBundle{}, errors.New("suite artifact manifest digest is not allowed by this tokenbench binary")
	}
	rootFile, rootInfo, err := openAndPinArtifactRoot(root, nil)
	if err != nil {
		return preparedArtifactBundle{}, err
	}
	defer rootFile.Close()
	raw, err := readArtifactManifestFile(rootFile)
	if err != nil {
		return preparedArtifactBundle{}, err
	}
	digestValue := SHA256(raw)
	if digestValue != loaded.suite.ArtifactManifestSHA256 || digestValue != policyDigest {
		return preparedArtifactBundle{}, errors.New("artifact manifest bytes differ from suite or binary policy")
	}
	manifest, err := decodeArtifactManifest(raw)
	if err != nil {
		return preparedArtifactBundle{}, err
	}
	audit := ArtifactBundleAudit{
		SchemaVersion: ArtifactBundleAuditVersion,
		Root:          root, Manifest: manifest, RawManifest: append([]byte(nil), raw...),
		ManifestSHA256: digestValue, Provenance: manifest.Provenance,
	}
	state := &preparedArtifactBundleState{
		audit: audit, suiteSHA256: loaded.sha256, rootInfo: rootInfo,
	}
	bundle := preparedArtifactBundle{state: state}
	origins, err := bundle.reverify(loaded)
	if err != nil {
		return preparedArtifactBundle{}, err
	}
	if origins.Codex.Path != loaded.suite.HarnessExecutable ||
		origins.Codex.SHA256 != loaded.suite.HarnessSHA256 {
		return preparedArtifactBundle{}, errors.New("suite Codex path or digest differs from artifact manifest")
	}
	if origins.Git.Path != loaded.suite.GitExecutable ||
		origins.Git.SHA256 != loaded.suite.GitExecutableSHA256 {
		return preparedArtifactBundle{}, errors.New("suite Git path or digest differs from artifact manifest")
	}
	return bundle, nil
}

func artifactBuildPolicyDigest() (string, error) {
	if trustedArtifactManifestSHA256 == "" {
		return "", errors.New("tokenbench binary has no trusted artifact manifest digest")
	}
	if !ValidSHA256(trustedArtifactManifestSHA256) {
		return "", errors.New("tokenbench binary trusted artifact manifest digest is malformed")
	}
	return trustedArtifactManifestSHA256, nil
}

func (bundle preparedArtifactBundle) reverify(loaded LoadedSuite) (artifactOriginSet, error) {
	if bundle.state == nil {
		return artifactOriginSet{}, errors.New("artifact bundle was not produced by LoadArtifactBundle")
	}
	state := bundle.state
	if loaded.sha256 != state.suiteSHA256 || SHA256(loaded.raw) != state.suiteSHA256 {
		return artifactOriginSet{}, errors.New("artifact bundle capability belongs to a different suite")
	}
	if err := state.audit.Validate(); err != nil {
		return artifactOriginSet{}, err
	}
	if loaded.suite.ArtifactManifestSHA256 != state.audit.ManifestSHA256 {
		return artifactOriginSet{}, errors.New("suite artifact manifest commitment changed")
	}
	policyDigest, err := artifactBuildPolicyDigest()
	if err != nil || policyDigest != state.audit.ManifestSHA256 {
		return artifactOriginSet{}, errors.Join(errors.New("artifact bundle is outside the binary build policy"), err)
	}
	root, _, err := openAndPinArtifactRoot(state.audit.Root, state.rootInfo)
	if err != nil {
		return artifactOriginSet{}, err
	}
	defer root.Close()
	raw, err := readArtifactManifestFile(root)
	if err != nil {
		return artifactOriginSet{}, err
	}
	if !bytes.Equal(raw, state.audit.RawManifest) {
		return artifactOriginSet{}, errors.New("artifact manifest changed after strict loading")
	}
	origins, err := verifyArtifactFiles(root, state.audit.Root, state.audit.Manifest)
	if err != nil {
		return artifactOriginSet{}, err
	}
	if origins.Codex.Path != loaded.suite.HarnessExecutable ||
		origins.Codex.SHA256 != loaded.suite.HarnessSHA256 ||
		origins.Git.Path != loaded.suite.GitExecutable ||
		origins.Git.SHA256 != loaded.suite.GitExecutableSHA256 {
		return artifactOriginSet{}, errors.New("suite Codex/Git identity differs from artifact bundle")
	}
	return origins, nil
}

func openAndPinArtifactRoot(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, nil, errors.Join(errors.New("artifact bundle root is not a real directory"), err)
	}
	if expected != nil && !os.SameFile(expected, before) {
		return nil, nil, errors.New("artifact bundle root changed after strict loading")
	}
	root, err := openArtifactRootNoSymlinks(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open artifact bundle root without symlinks: %w", err)
	}
	opened, err := root.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		root.Close()
		return nil, nil, errors.Join(errors.New("artifact bundle root changed while opening"), err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, nil, errors.Join(errors.New("artifact bundle root changed while pinning"), err)
	}
	return root, opened, nil
}

func readArtifactManifestFile(root *os.File) ([]byte, error) {
	first, err := openArtifactFileNoSymlinks(root, ArtifactManifestFilename)
	if err != nil {
		return nil, fmt.Errorf("open artifact manifest: %w", err)
	}
	raw, firstInfo, err := readStableOpenFile(first, maximumArtifactManifestBytes, false)
	closeErr := first.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	second, err := openArtifactFileNoSymlinks(root, ArtifactManifestFilename)
	if err != nil {
		return nil, err
	}
	secondRaw, secondInfo, readErr := readStableOpenFile(second, maximumArtifactManifestBytes, false)
	closeErr = second.Close()
	if readErr != nil || closeErr != nil || !os.SameFile(firstInfo, secondInfo) ||
		!bytes.Equal(raw, secondRaw) {
		return nil, errors.Join(errors.New("artifact manifest changed while loading"), readErr, closeErr)
	}
	return raw, nil
}

func verifyArtifactFiles(
	root *os.File,
	rootPath string,
	manifest ArtifactManifest,
) (artifactOriginSet, error) {
	if err := manifest.Validate(); err != nil {
		return artifactOriginSet{}, err
	}
	verified := make(map[string]executionsnapshot.FileOrigin, len(manifest.roles()))
	var totalBytes int64
	for _, role := range manifest.roles() {
		origin, size, err := verifyArtifactFile(root, rootPath, role)
		if err != nil {
			return artifactOriginSet{}, err
		}
		if totalBytes > maximumArtifactBundleBytes-size {
			return artifactOriginSet{}, errors.New("artifact bundle exceeds its total byte limit")
		}
		totalBytes += size
		verified[role.name] = origin
	}
	return artifactOriginSet{
		Codex: verified["codex"], RepoView: verified["repo-view"],
		Git: verified["static Git"], Bash: verified["static Bash"],
		Utilities: executionsnapshot.UtilityOrigins{
			Ripgrep: verified["rg"], Sed: verified["sed"], Awk: verified["awk"],
			Find: verified["find"], Head: verified["head"], Tail: verified["tail"],
			WC: verified["wc"], Sort: verified["sort"], Cut: verified["cut"],
			Tr: verified["tr"], Cat: verified["cat"], LS: verified["ls"],
			Grep: verified["grep"], Xargs: verified["xargs"],
		},
	}, nil
}

func verifyArtifactFile(
	root *os.File,
	rootPath string,
	role artifactRole,
) (executionsnapshot.FileOrigin, int64, error) {
	first, err := openArtifactFileNoSymlinks(root, role.file.Path)
	if err != nil {
		return executionsnapshot.FileOrigin{}, 0, fmt.Errorf("open artifact %s: %w", role.name, err)
	}
	digestValue, firstInfo, err := identifyStableStaticELF(first)
	closeErr := first.Close()
	if err != nil || closeErr != nil {
		return executionsnapshot.FileOrigin{}, 0, errors.Join(
			fmt.Errorf("verify artifact %s", role.name), err, closeErr,
		)
	}
	if digestValue != role.file.SHA256 {
		return executionsnapshot.FileOrigin{}, 0, fmt.Errorf("artifact %s digest mismatch", role.name)
	}
	second, err := openArtifactFileNoSymlinks(root, role.file.Path)
	if err != nil {
		return executionsnapshot.FileOrigin{}, 0, err
	}
	secondDigest, secondInfo, verifyErr := identifyStableStaticELF(second)
	closeErr = second.Close()
	if verifyErr != nil || closeErr != nil || !os.SameFile(firstInfo, secondInfo) || secondDigest != digestValue {
		return executionsnapshot.FileOrigin{}, 0, errors.Join(
			fmt.Errorf("artifact %s changed while loading", role.name), verifyErr, closeErr,
		)
	}
	absolute := filepath.Join(rootPath, filepath.FromSlash(role.file.Path))
	if !pathWithinRoot(rootPath, absolute) {
		return executionsnapshot.FileOrigin{}, 0, fmt.Errorf("artifact %s escaped bundle root", role.name)
	}
	return executionsnapshot.FileOrigin{Path: absolute, SHA256: digestValue}, firstInfo.Size(), nil
}

func identifyStableStaticELF(file *os.File) (string, os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 ||
		hasMultipleLinks(before) || before.Size() <= 0 || before.Size() > maximumArtifactFileBytes {
		return "", nil, errors.Join(errors.New("artifact is not a bounded single-link executable file"), err)
	}
	parsed, err := elf.NewFile(file)
	if err != nil {
		return "", nil, fmt.Errorf("parse artifact ELF: %w", err)
	}
	for _, program := range parsed.Progs {
		if program.Type == elf.PT_INTERP {
			return "", nil, errors.New("artifact ELF has a dynamic interpreter")
		}
	}
	needed, err := parsed.DynString(elf.DT_NEEDED)
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return "", nil, fmt.Errorf("inspect artifact ELF dependencies: %w", err)
	}
	if len(needed) != 0 {
		return "", nil, errors.New("artifact ELF has dynamic dependencies")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: maximumArtifactFileBytes + 1}
	written, err := io.CopyBuffer(hasher, limited, make([]byte, 128<<10))
	if err != nil || written != before.Size() || limited.N == 0 {
		return "", nil, errors.Join(errors.New("artifact size changed or exceeded its limit"), err)
	}
	after, err := file.Stat()
	if err != nil || !sameArtifactFile(before, after) {
		return "", nil, errors.Join(errors.New("artifact changed while hashing"), err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), after, nil
}

func readStableOpenFile(file *os.File, maximum int64, executable bool) ([]byte, os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || hasMultipleLinks(before) ||
		before.Size() < 0 || before.Size() > maximum || executable && before.Mode().Perm()&0o111 == 0 {
		return nil, nil, errors.Join(errors.New("bundle file is not a bounded single-link regular file"), err)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum || int64(len(content)) != before.Size() {
		return nil, nil, errors.Join(errors.New("bundle file changed or exceeded its limit"), err)
	}
	after, err := file.Stat()
	if err != nil || !sameArtifactFile(before, after) {
		return nil, nil, errors.Join(errors.New("bundle file changed while reading"), err)
	}
	return content, after, nil
}

func sameArtifactFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Validate verifies the exact raw/structured audit commitment without probing
// files. Publishable plan validation separately binds it to OriginInputs.
func (audit ArtifactBundleAudit) Validate() error {
	if audit.SchemaVersion != ArtifactBundleAuditVersion {
		return fmt.Errorf("unexpected artifact bundle audit schema %q", audit.SchemaVersion)
	}
	if !filepath.IsAbs(audit.Root) || filepath.Clean(audit.Root) != audit.Root ||
		audit.Root == "/" || len(audit.Root) > maximumSuitePathBytes {
		return errors.New("artifact bundle audit root is invalid")
	}
	manifest, err := decodeArtifactManifest(audit.RawManifest)
	if err != nil {
		return err
	}
	if !ValidSHA256(audit.ManifestSHA256) || SHA256(audit.RawManifest) != audit.ManifestSHA256 {
		return errors.New("artifact bundle audit manifest digest is invalid")
	}
	if !reflect.DeepEqual(manifest, audit.Manifest) ||
		!reflect.DeepEqual(manifest.Provenance, audit.Provenance) {
		return errors.New("artifact bundle audit structured data differs from exact manifest bytes")
	}
	return nil
}

func (audit ArtifactBundleAudit) validateBinding(origins executionsnapshot.OriginInputs) error {
	if err := audit.Validate(); err != nil {
		return err
	}
	roles := audit.Manifest.roles()
	want := make(map[string]executionsnapshot.FileOrigin, len(roles))
	for _, role := range roles {
		want[role.name] = executionsnapshot.FileOrigin{
			Path:   filepath.Join(audit.Root, filepath.FromSlash(role.file.Path)),
			SHA256: role.file.SHA256,
		}
	}
	actual := map[string]executionsnapshot.FileOrigin{
		"codex": origins.Codex, "repo-view": origins.RepoView,
		"static Git": origins.Git, "static Bash": origins.Bash,
		"rg": origins.Utilities.Ripgrep, "sed": origins.Utilities.Sed,
		"awk": origins.Utilities.Awk, "find": origins.Utilities.Find,
		"head": origins.Utilities.Head, "tail": origins.Utilities.Tail,
		"wc": origins.Utilities.WC, "sort": origins.Utilities.Sort,
		"cut": origins.Utilities.Cut, "tr": origins.Utilities.Tr,
		"cat": origins.Utilities.Cat, "ls": origins.Utilities.LS,
		"grep": origins.Utilities.Grep, "xargs": origins.Utilities.Xargs,
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if actual[name] != want[name] {
			return fmt.Errorf("artifact bundle role %s differs from origin inputs", name)
		}
	}
	return nil
}

func cloneArtifactBundleAudit(audit ArtifactBundleAudit) ArtifactBundleAudit {
	clone := audit
	clone.RawManifest = append([]byte(nil), audit.RawManifest...)
	return clone
}
