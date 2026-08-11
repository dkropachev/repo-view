package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/cas"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/evidence"
	"golang.org/x/sys/unix"
)

type secretDescriptor struct {
	value int
	set   bool
}

func (descriptor *secretDescriptor) String() string {
	if descriptor == nil || !descriptor.set {
		return ""
	}
	return strconv.Itoa(descriptor.value)
}

func (descriptor *secretDescriptor) Set(raw string) error {
	if descriptor == nil || descriptor.set {
		return errors.New("credential descriptor must be supplied exactly once")
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 3 || value > 255 || strconv.Itoa(value) != raw {
		return errors.New("credential descriptor must be canonical decimal in [3,255]")
	}
	descriptor.value = value
	descriptor.set = true
	return nil
}

const (
	maxCredentialBytes     = 8 << 10
	maxTrustPolicyBytes    = 64 << 10
	maxRootRefBytes        = 4 << 10
	maxRecoveryBytes       = 16 << 10
	credentialPipeTimeout  = 5 * time.Second
	attestationRootType    = "application/vnd.tokenbench.attestation.v2+json"
	recoverySchema         = "tokenbench.publication-recovery/v2"
	recoveryCompleteOutput = "complete_root_output_not_finalized"
	recoveryIncomplete     = "publication_incomplete"
)

type publicationRecoveryRecord struct {
	SchemaVersion    string                     `json:"schema_version"`
	Status           string                     `json:"status"`
	BundleKind       string                     `json:"bundle_kind"`
	IntendedRootPath string                     `json:"intended_root_path"`
	Publication      evidence.PublicationResult `json:"publication"`
}

type publishedRootError struct {
	RootPath     string
	RecoveryPath string
	Cause        error
	Root         cas.ObjectRef
}

func (failure *publishedRootError) Error() string {
	if failure == nil {
		return "published tokenbench root reported a follow-up failure"
	}
	switch {
	case failure.RootPath != "":
		return fmt.Sprintf(
			"signed root %s is known published and recorded at %s, but publication reported: %v",
			failure.Root.Digest,
			failure.RootPath,
			failure.Cause,
		)
	case failure.RecoveryPath != "":
		return fmt.Sprintf(
			"signed root %s is known published; recovery was recorded at %s: %v",
			failure.Root.Digest,
			failure.RecoveryPath,
			failure.Cause,
		)
	default:
		return fmt.Sprintf(
			"signed root %s is known published, but its root reference and recovery record could not be finalized: %v",
			failure.Root.Digest,
			failure.Cause,
		)
	}
}

func (failure *publishedRootError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

type publicationRecoveryError struct {
	RecoveryPath string
	Cause        error
	Result       evidence.PublicationResult
}

func (failure *publicationRecoveryError) Error() string {
	if failure == nil {
		return "tokenbench publication requires recovery"
	}
	if failure.RecoveryPath != "" {
		return fmt.Sprintf(
			"tokenbench publication state %q requires recovery; recovery was recorded at %s: %v",
			failure.Result.State,
			failure.RecoveryPath,
			failure.Cause,
		)
	}
	return fmt.Sprintf(
		"tokenbench publication state %q requires recovery and its recovery record could not be finalized: %v",
		failure.Result.State,
		failure.Cause,
	)
}

func (failure *publicationRecoveryError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

var (
	errCredentialSource = errors.New(
		"upstream credential source is unavailable, unsafe, or noncanonical",
	)
	errSigningKeySource = errors.New(
		"Ed25519 signing-key source is unavailable, unsafe, or noncanonical",
	)
)

type runPaths struct {
	StateRoot          string
	SnapshotRoot       string
	ArtifactBundleRoot string
	CAS                string
	RootOutput         string
	SigningKeyFile     string
	TrustPolicy        string
	SourceRoot         string
}

type namedPath struct {
	name string
	path string
}

func resolveRunPaths(raw runPaths) (runPaths, error) {
	var result runPaths
	var err error
	if result.StateRoot, err = requireAbsoluteClean(raw.StateRoot, "Codex state root"); err != nil {
		return runPaths{}, err
	}
	if result.SnapshotRoot, err = requireAbsoluteClean(raw.SnapshotRoot, "execution snapshot root"); err != nil {
		return runPaths{}, err
	}
	if result.ArtifactBundleRoot, err = requireAbsoluteClean(
		raw.ArtifactBundleRoot,
		"execution artifact bundle root",
	); err != nil {
		return runPaths{}, err
	}
	if result.CAS, err = requireAbsoluteClean(raw.CAS, "evidence CAS"); err != nil {
		return runPaths{}, err
	}
	if result.RootOutput, err = requireAbsoluteClean(raw.RootOutput, "signed root output"); err != nil {
		return runPaths{}, err
	}
	if result.SigningKeyFile, err = requireAbsoluteClean(raw.SigningKeyFile, "signing key file"); err != nil {
		return runPaths{}, err
	}
	if result.TrustPolicy, err = requireAbsoluteClean(raw.TrustPolicy, "trust policy"); err != nil {
		return runPaths{}, err
	}
	if result.SourceRoot, err = requireAbsoluteClean(raw.SourceRoot, "suite source root"); err != nil {
		return runPaths{}, err
	}
	recoveryPath, err := publicationRecoveryPath(result.RootOutput)
	if err != nil {
		return runPaths{}, err
	}
	named := []namedPath{
		{"Codex state root", result.StateRoot},
		{"execution snapshot root", result.SnapshotRoot},
		{"execution artifact bundle root", result.ArtifactBundleRoot},
		{"evidence CAS", result.CAS},
		{"signed root output", result.RootOutput},
		{"publication recovery output", recoveryPath},
		{"signing key file", result.SigningKeyFile},
		{"trust policy", result.TrustPolicy},
		{"suite source root", result.SourceRoot},
	}
	if err := requireDisjointPaths(named); err != nil {
		return runPaths{}, err
	}
	return result, nil
}

func requireDisjointPaths(named []namedPath) error {
	for left := range named {
		for right := left + 1; right < len(named); right++ {
			if pathsOverlap(named[left].path, named[right].path) {
				return fmt.Errorf(
					"%s and %s paths must be disjoint",
					named[left].name,
					named[right].name,
				)
			}
		}
	}
	return requirePhysicallyDisjointPaths(named)
}

func canonicalCommandPaths(raw []namedPath) ([]namedPath, error) {
	resolved := make([]namedPath, len(raw))
	for index, path := range raw {
		clean, err := requireAbsoluteClean(path.path, path.name)
		if err != nil {
			return nil, err
		}
		resolved[index] = namedPath{name: path.name, path: clean}
	}
	if err := requireDisjointPaths(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func requireAbsoluteClean(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		path == filepath.VolumeName(path)+string(filepath.Separator) {
		return "", fmt.Errorf("%s must be an absolute, clean, non-root path", label)
	}
	return path, nil
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return true
	}
	return relative == "." || relative != ".." &&
		!bytes.HasPrefix([]byte(relative), []byte(".."+string(filepath.Separator)))
}

func claimPrivateDirectory(path, label string) error {
	clean, err := requireAbsoluteClean(path, label)
	if err != nil {
		return err
	}
	if err := requireRealParent(clean, label); err != nil {
		return err
	}
	if err := os.Mkdir(clean, 0o700); err != nil {
		return fmt.Errorf("claim new exclusive %s: %w", label, err)
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return fmt.Errorf("claimed %s is not a private real directory", label)
	}
	return nil
}

func openCAS(path string) (*cas.Store, error) {
	clean, err := requireAbsoluteClean(path, "evidence CAS")
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return nil, errors.New("evidence CAS path must not traverse symbolic links")
	}
	return cas.Open(clean, cas.Options{MaxObjectBytes: maxCASObjectBytes})
}

func loadCredentialFD(descriptor int) ([]byte, error) {
	return loadCredentialFDWithin(descriptor, credentialPipeTimeout)
}

func loadCredentialFDWithin(descriptor int, pipeTimeout time.Duration) ([]byte, error) {
	if descriptor < 3 || descriptor > 255 {
		return nil, errCredentialSource
	}
	file := os.NewFile(uintptr(descriptor), "tokenbench-credential-source")
	if file == nil {
		return nil, errCredentialSource
	}
	closeSource := func() error {
		if file == nil {
			return nil
		}
		err := file.Close()
		file = nil
		return err
	}
	fail := func() error {
		_ = closeSource()
		return errCredentialSource
	}
	statusFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || statusFlags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, fail()
	}
	descriptorFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil {
		return nil, fail()
	}
	if _, err := unix.FcntlInt(
		file.Fd(),
		unix.F_SETFD,
		descriptorFlags|unix.FD_CLOEXEC,
	); err != nil {
		return nil, fail()
	}
	before, statErr := file.Stat()
	if statErr != nil || !validCredentialDescriptorInfo(before) {
		return nil, fail()
	}
	regular := before.Mode().IsRegular()
	if regular {
		offset, seekErr := file.Seek(0, io.SeekCurrent)
		if seekErr != nil || offset != 0 {
			return nil, fail()
		}
	}
	var raw []byte
	var readErr error
	if regular {
		raw, readErr = io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	} else {
		if _, err := unix.FcntlInt(
			file.Fd(),
			unix.F_SETFL,
			statusFlags|unix.O_NONBLOCK,
		); err != nil {
			return nil, fail()
		}
		raw, readErr = readCredentialPipe(int(file.Fd()), pipeTimeout)
	}
	after, statAfterErr := file.Stat()
	if statAfterErr != nil || !os.SameFile(before, after) ||
		before.Size() != after.Size() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) {
		readErr = errors.New("credential source changed")
	}
	closeErr := closeSource()
	if readErr != nil || closeErr != nil || len(raw) == 0 ||
		len(raw) > maxCredentialBytes || int64(len(raw)) != before.Size() && regular {
		clear(raw)
		return nil, errCredentialSource
	}
	defer clear(raw)
	for _, character := range raw {
		if character < 0x21 || character > 0x7e {
			return nil, errCredentialSource
		}
	}
	result := append([]byte(nil), raw...)
	return result, nil
}

func validCredentialDescriptorInfo(info fs.FileInfo) bool {
	if info == nil || info.Size() < 0 || info.Size() > maxCredentialBytes {
		return false
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeNamedPipe == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	permissions := info.Mode().Perm()
	return stat.Nlink == 1 && permissions&0o400 != 0 &&
		permissions&^fs.FileMode(0o600) == 0
}

func readCredentialPipe(descriptor int, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		return nil, errors.New("credential pipe deadline is invalid")
	}
	deadline := time.Now().Add(timeout)
	raw := make([]byte, 0, maxCredentialBytes)
	for {
		remainingCapacity := maxCredentialBytes + 1 - len(raw)
		if remainingCapacity <= 0 {
			clear(raw)
			return nil, errors.New("credential pipe exceeds its byte limit")
		}
		bufferSize := remainingCapacity
		if bufferSize > 4096 {
			bufferSize = 4096
		}
		buffer := make([]byte, bufferSize)
		count, readErr := unix.Read(descriptor, buffer)
		if count > 0 {
			raw = append(raw, buffer[:count]...)
			clear(buffer)
			if len(raw) > maxCredentialBytes {
				clear(raw)
				return nil, errors.New("credential pipe exceeds its byte limit")
			}
			continue
		}
		clear(buffer)
		switch {
		case readErr == nil:
			return raw, nil
		case errors.Is(readErr, unix.EINTR):
			continue
		case !errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, unix.EWOULDBLOCK):
			clear(raw)
			return nil, readErr
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			clear(raw)
			return nil, errors.New("credential pipe deadline expired")
		}
		milliseconds := (remaining + time.Millisecond - 1) / time.Millisecond
		if milliseconds > time.Duration(int(^uint32(0)>>1)) {
			milliseconds = time.Duration(int(^uint32(0) >> 1))
		}
		pollDescriptors := []unix.PollFd{{
			Fd:     int32(descriptor),
			Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR,
		}}
		ready, pollErr := unix.Poll(pollDescriptors, int(milliseconds))
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil {
			clear(raw)
			return nil, pollErr
		}
		if ready == 0 {
			clear(raw)
			return nil, errors.New("credential pipe deadline expired")
		}
		if pollDescriptors[0].Revents&unix.POLLNVAL != 0 {
			clear(raw)
			return nil, errors.New("credential pipe descriptor became invalid")
		}
	}
}

func loadEd25519Signer(path string) (*evidence.Ed25519Signer, error) {
	raw, err := readStableFile(path, 128, true)
	if err != nil {
		clear(raw)
		return nil, errSigningKeySource
	}
	defer clear(raw)
	seed, err := base64.RawURLEncoding.DecodeString(string(raw))
	if err != nil || len(seed) != ed25519.SeedSize ||
		base64.RawURLEncoding.EncodeToString(seed) != string(raw) {
		clear(seed)
		return nil, errSigningKeySource
	}
	defer clear(seed)
	privateKey := ed25519.NewKeyFromSeed(seed)
	defer clear(privateKey)
	signer, err := evidence.NewEd25519Signer(privateKey)
	if err != nil {
		return nil, errSigningKeySource
	}
	return signer, nil
}

func loadTrustPolicy(path string) (*evidence.Verifier, error) {
	raw, err := readStableFile(path, maxTrustPolicyBytes, false)
	if err != nil {
		return nil, fmt.Errorf("read explicit trust policy: %w", err)
	}
	verifier, err := evidence.DecodeTrustPolicy(raw)
	if err != nil {
		return nil, fmt.Errorf("decode explicit trust policy: %w", err)
	}
	return verifier, nil
}

func readCanonicalRoot(path string) (cas.ObjectRef, error) {
	raw, err := readStableFile(path, maxRootRefBytes, false)
	if err != nil {
		return cas.ObjectRef{}, fmt.Errorf("read signed root reference: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var root cas.ObjectRef
	if err := decoder.Decode(&root); err != nil {
		return cas.ObjectRef{}, errors.New("signed root reference is not strict canonical JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return cas.ObjectRef{}, errors.New("signed root reference is not strict canonical JSON")
	}
	canonical, err := canonicalRoot(root)
	if err != nil || !bytes.Equal(raw, canonical) {
		return cas.ObjectRef{}, errors.New("signed root reference is not strict canonical JSON")
	}
	return root, nil
}

func canonicalRoot(root cas.ObjectRef) ([]byte, error) {
	if err := root.Validate(); err != nil {
		return nil, err
	}
	if root.MediaType != attestationRootType {
		return nil, errors.New("conformant root must reference a signed attestation envelope")
	}
	return json.Marshal(root)
}

func finalizablePublication(result evidence.PublicationResult) bool {
	if result.Validate() != nil || result.State != evidence.PublicationComplete || !result.Durable ||
		!result.GraphVerified || result.RecoveryRequired {
		return false
	}
	_, err := canonicalRoot(result.IntendedRoot)
	return err == nil
}

func publicationRecoveryPath(rootPath string) (string, error) {
	rootPath, err := requireAbsoluteClean(rootPath, "intended signed root output")
	if err != nil {
		return "", err
	}
	return requireAbsoluteClean(rootPath+".recovery.json", "publication recovery output")
}

func finalizeCompletePublication(
	output *exclusiveOutput,
	rootPath string,
	bundleKind evidence.BundleKind,
	result evidence.PublicationResult,
	cause error,
) error {
	if !finalizablePublication(result) {
		return errors.New("publication result is not a finalizable complete graph")
	}
	root := result.IntendedRoot
	commitErr := output.commitCanonicalRoot(root)
	joined := errors.Join(cause, commitErr)
	if joined == nil {
		return nil
	}
	rootVisible := commitErr == nil
	var visibilityErr error
	if !rootVisible {
		observed, err := readCanonicalRoot(rootPath)
		switch {
		case err == nil && observed == root:
			rootVisible = true
		case err != nil:
			visibilityErr = fmt.Errorf("verify intended signed-root output: %w", err)
		default:
			visibilityErr = errors.New("intended signed-root output contains a different root")
		}
	}
	if rootVisible {
		return &publishedRootError{
			Root:     root,
			RootPath: rootPath,
			Cause:    joined,
		}
	}
	recoveryPath, recoveryErr := writePublicationRecovery(
		rootPath,
		bundleKind,
		recoveryCompleteOutput,
		result,
	)
	return &publishedRootError{
		Root:         root,
		RecoveryPath: recoveryPath,
		Cause: errors.Join(
			joined,
			visibilityErr,
			wrapRecoveryError(recoveryErr),
		),
	}
}

func finalizeIncompletePublication(
	rootPath string,
	bundleKind evidence.BundleKind,
	result evidence.PublicationResult,
	cause error,
) error {
	if finalizablePublication(result) {
		return errors.New("complete publication must use canonical root finalization")
	}
	recoveryPath, recoveryErr := writePublicationRecovery(
		rootPath,
		bundleKind,
		recoveryIncomplete,
		result,
	)
	if cause == nil {
		cause = fmt.Errorf("publication remained in state %q", result.State)
	}
	return &publicationRecoveryError{
		Result:       result,
		RecoveryPath: recoveryPath,
		Cause:        errors.Join(cause, wrapRecoveryError(recoveryErr)),
	}
}

func wrapRecoveryError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("finalize publication recovery record: %w", err)
}

func writePublicationRecovery(
	rootPath string,
	bundleKind evidence.BundleKind,
	status string,
	publication evidence.PublicationResult,
) (string, error) {
	if err := publication.Validate(); err != nil {
		return "", fmt.Errorf("validate publication recovery result: %w", err)
	}
	if status != recoveryCompleteOutput && status != recoveryIncomplete {
		return "", errors.New("invalid publication recovery status")
	}
	if status == recoveryCompleteOutput && !finalizablePublication(publication) {
		return "", errors.New("complete-output recovery requires a complete publication")
	}
	if status == recoveryIncomplete && finalizablePublication(publication) {
		return "", errors.New("incomplete recovery cannot contain a complete publication")
	}
	cleanRoot, err := requireAbsoluteClean(rootPath, "intended signed root output")
	if err != nil {
		return "", err
	}
	recoveryPath, err := publicationRecoveryPath(cleanRoot)
	if err != nil {
		return "", err
	}
	record := publicationRecoveryRecord{
		SchemaVersion:    recoverySchema,
		Status:           status,
		BundleKind:       string(bundleKind),
		IntendedRootPath: cleanRoot,
		Publication:      publication,
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxRecoveryBytes {
		return "", errors.New("encode bounded publication recovery record")
	}
	output, err := claimExclusiveOutput(
		recoveryPath,
		0o600,
		"publication recovery record",
	)
	if err != nil {
		return "", err
	}
	defer output.abort()
	if err := output.commit(raw); err != nil {
		observed, readErr := readStableFile(recoveryPath, maxRecoveryBytes, false)
		if readErr == nil && bytes.Equal(observed, raw) {
			return recoveryPath, err
		}
		return "", errors.Join(err, readErr)
	}
	return recoveryPath, nil
}

func readStableFile(path string, maximum int64, ownerOnly bool) ([]byte, error) {
	clean, err := requireAbsoluteClean(path, "input file")
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return nil, errors.New("input file path must not traverse symbolic links")
	}
	before, err := os.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if err := validateStableFileInfo(before, maximum, ownerOnly); err != nil {
		return nil, err
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("input file changed while it was opened")
	}
	if err := validateStableFileInfo(opened, maximum, ownerOnly); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("input file exceeds its byte limit")
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if err := validateStableFileInfo(openedAfter, maximum, ownerOnly); err != nil {
		return nil, err
	}
	if err := validateStableFileInfo(after, maximum, ownerOnly); err != nil {
		return nil, err
	}
	if !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		before.Mode() != openedAfter.Mode() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("input file changed while it was read")
	}
	return raw, nil
}

func validateStableFileInfo(info fs.FileInfo, maximum int64, ownerOnly bool) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximum {
		return errors.New("input file must be one bounded regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("input file must have exactly one filesystem link")
	}
	if ownerOnly {
		permissions := info.Mode().Perm()
		if stat.Uid != uint32(os.Geteuid()) || permissions&0o400 == 0 ||
			permissions & ^fs.FileMode(0o600) != 0 {
			return errors.New("secret file must be caller-owned with mode 0600 or stricter")
		}
	}
	return nil
}

type exclusiveOutput struct {
	parent    *os.File
	file      *os.File
	path      string
	name      string
	staging   string
	mode      os.FileMode
	published bool
	complete  bool
}

func claimExclusiveOutput(
	path string,
	mode os.FileMode,
	label string,
) (*exclusiveOutput, error) {
	clean, err := requireAbsoluteClean(path, label)
	if err != nil {
		return nil, err
	}
	if err := requireRealParent(clean, label); err != nil {
		return nil, err
	}
	parentPath := filepath.Dir(clean)
	parentBefore, err := os.Lstat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s parent: %w", label, err)
	}
	parentDescriptor, err := unix.Open(
		parentPath,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s parent: %w", label, err)
	}
	parent := os.NewFile(uintptr(parentDescriptor), parentPath)
	if parent == nil {
		_ = unix.Close(parentDescriptor)
		return nil, fmt.Errorf("open %s parent", label)
	}
	parentOpened, err := parent.Stat()
	if err != nil || !os.SameFile(parentBefore, parentOpened) {
		parent.Close()
		return nil, fmt.Errorf("%s parent changed while opening", label)
	}
	finalName := filepath.Base(clean)
	if _, err := os.Lstat(clean); err == nil || !errors.Is(err, os.ErrNotExist) {
		parent.Close()
		if err == nil {
			err = fs.ErrExist
		}
		return nil, fmt.Errorf("claim new exclusive %s: %w", label, err)
	}
	var staging string
	descriptor := -1
	for range 32 {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			parent.Close()
			return nil, fmt.Errorf("create %s staging name", label)
		}
		staging = "." + finalName + ".tokenbench-staging-" + hex.EncodeToString(nonce)
		descriptor, err = unix.Openat(
			int(parent.Fd()),
			staging,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EEXIST) {
			parent.Close()
			return nil, fmt.Errorf("claim private %s staging file: %w", label, err)
		}
	}
	if descriptor < 0 || err != nil {
		parent.Close()
		return nil, fmt.Errorf("claim private %s staging file", label)
	}
	file := os.NewFile(uintptr(descriptor), staging)
	return &exclusiveOutput{
		path:    clean,
		name:    finalName,
		staging: staging,
		parent:  parent,
		file:    file,
		mode:    mode,
	}, nil
}

func (output *exclusiveOutput) commitCanonicalRoot(root cas.ObjectRef) error {
	raw, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	return output.commit(raw)
}

func (output *exclusiveOutput) commit(raw []byte) error {
	if output == nil || output.file == nil || output.parent == nil || output.complete {
		return errors.New("exclusive output is not writable")
	}
	if len(raw) == 0 {
		return errors.New("exclusive output must not be empty")
	}
	written, err := output.file.Write(raw)
	if err != nil || written != len(raw) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return fmt.Errorf("write exclusive output: %w", err)
	}
	if err := output.file.Chmod(output.mode); err != nil {
		return fmt.Errorf("set exclusive output mode: %w", err)
	}
	if err := output.file.Sync(); err != nil {
		return fmt.Errorf("sync exclusive output: %w", err)
	}
	if err := output.file.Close(); err != nil {
		output.file = nil
		return fmt.Errorf("close exclusive output: %w", err)
	}
	output.file = nil
	parentPath := filepath.Dir(output.path)
	parentBefore, err := os.Lstat(parentPath)
	if err != nil {
		return fmt.Errorf("restat exclusive output parent before publication: %w", err)
	}
	parentOpened, err := output.parent.Stat()
	if err != nil || !os.SameFile(parentBefore, parentOpened) {
		return errors.New("exclusive output parent changed before publication")
	}
	if err := unix.Renameat2(
		int(output.parent.Fd()),
		output.staging,
		int(output.parent.Fd()),
		output.name,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return fmt.Errorf("publish exclusive output without replacement: %w", err)
	}
	output.published = true
	output.staging = ""
	if err := output.parent.Sync(); err != nil {
		return fmt.Errorf("sync exclusive output directory: %w", err)
	}
	parentAfter, err := os.Lstat(parentPath)
	if err != nil {
		return fmt.Errorf("restat exclusive output parent: %w", err)
	}
	parentOpened, err = output.parent.Stat()
	if err != nil || !os.SameFile(parentAfter, parentOpened) {
		return errors.New("exclusive output parent changed during publication")
	}
	output.complete = true
	if err := output.parent.Close(); err != nil {
		output.parent = nil
		return fmt.Errorf("close exclusive output parent: %w", err)
	}
	output.parent = nil
	return nil
}

func (output *exclusiveOutput) abort() {
	if output == nil {
		return
	}
	if output.file != nil {
		_ = output.file.Close()
		output.file = nil
	}
	if output.parent != nil {
		if output.staging != "" {
			_ = unix.Unlinkat(int(output.parent.Fd()), output.staging, 0)
			_ = output.parent.Sync()
		}
		_ = output.parent.Close()
		output.parent = nil
	}
}

func requireRealParent(path, label string) error {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("%s parent must be an existing real canonical directory", label)
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s parent must be an existing real canonical directory", label)
	}
	return nil
}
