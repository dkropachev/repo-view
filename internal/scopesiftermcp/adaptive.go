package scopesiftermcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	adaptiveDefaultBudget = 1024
	adaptiveMaximumBudget = 3 * 1024
	adaptiveBudgetStep    = 512
	adaptiveRecentLimit   = 3
	adaptiveQuietLimit    = 8
	adaptiveRegionLimit   = 256
	adaptiveRegionExpiry  = 30 * 24 * time.Hour
	adaptiveStateVersion  = 1
	adaptiveStateMaxBytes = 1 << 20
)

const adaptiveCacheDirectory = "adaptive-output"

// adaptiveRequest is deliberately short-lived. Region paths and normalized
// arguments are used only to derive these hashes and are never persisted.
type adaptiveRequest struct {
	regionChain []string
	fingerprint [sha256.Size]byte
}

// adaptiveLearner tracks how much structured output a repository region has
// demonstrated that it needs. All mutation is synchronized because an MCP
// server may execute tool calls concurrently.
type adaptiveLearner struct {
	regions     map[string]*adaptiveRegion
	now         func() time.Time
	logger      *slog.Logger
	stateWriter func(adaptivePersistentState) error
	statePath   string
	recent      []adaptiveCompactedCall

	mu         sync.Mutex
	logFailure sync.Once
}

type adaptiveRegion struct {
	lastUsed   time.Time
	budget     int
	quietCalls int
}

type adaptiveCompactedCall struct {
	region      string
	fingerprint [sha256.Size]byte
	retried     bool
}

type adaptivePersistentState struct {
	Regions []adaptivePersistentRegion `json:"regions"`
	Version int                        `json:"version"`
}

type adaptivePersistentRegion struct {
	ID         string `json:"id"`
	Budget     int    `json:"budget"`
	QuietCalls int    `json:"quiet_calls"`
	LastUsed   int64  `json:"last_used"`
}

type adaptiveLearnerOptions struct {
	now    func() time.Time
	logger *slog.Logger

	cacheBase string

	persistent bool
}

// newAdaptiveLearner creates session-only state unless persistent is true. A
// persistent learner stores its heuristic state under the OS user-cache root.
func newAdaptiveLearner(
	repositoryRoot string,
	persistent bool,
	logger *slog.Logger,
) (*adaptiveLearner, error) {
	return newAdaptiveLearnerWithOptions(repositoryRoot, adaptiveLearnerOptions{
		persistent: persistent,
		logger:     logger,
	})
}

func newAdaptiveLearnerWithOptions(
	repositoryRoot string,
	options adaptiveLearnerOptions,
) (*adaptiveLearner, error) {
	clock := options.now
	if clock == nil {
		clock = time.Now
	}
	logger := options.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	learner := &adaptiveLearner{
		regions: make(map[string]*adaptiveRegion),
		now:     clock,
		logger:  logger,
	}
	if !options.persistent {
		return learner, nil
	}

	cacheBase := options.cacheBase
	if cacheBase == "" {
		var err error
		cacheBase, err = os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("locate MCP adaptive-output cache: %w", err)
		}
	}
	stateDirectory := filepath.Join(cacheBase, ImplementationName, adaptiveCacheDirectory)
	statePath := filepath.Join(stateDirectory, repositoryCacheKey(repositoryRoot)+".json")
	underCacheBase, err := pathContainedBy(cacheBase, statePath)
	if err != nil {
		return nil, fmt.Errorf("validate MCP adaptive-output cache root: %w", err)
	}
	if !underCacheBase {
		return nil, errors.New("MCP adaptive-output cache path escapes the user-cache directory")
	}
	inside, err := pathInsideRepository(repositoryRoot, statePath)
	if err != nil {
		return nil, fmt.Errorf("validate MCP adaptive-output cache path: %w", err)
	}
	if inside {
		return nil, errors.New("MCP adaptive-output cache must not be inside the repository")
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create MCP adaptive-output cache directory: %w", err)
	}
	if err := os.Chmod(stateDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("secure MCP adaptive-output cache directory: %w", err)
	}
	stateInfo, err := os.Lstat(statePath)
	if err == nil {
		if !stateInfo.Mode().IsRegular() {
			// A non-regular entry is corrupt state. Leave it unread; the first
			// successful atomic replacement will safely replace the entry itself.
		} else if err := os.Chmod(statePath, 0o600); err != nil {
			return nil, fmt.Errorf("secure MCP adaptive-output cache state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect MCP adaptive-output cache state: %w", err)
	}
	learner.statePath = statePath
	learner.stateWriter = learner.writeStateAtomic
	learner.loadState()
	return learner, nil
}

// budget returns the current budget and marks the exact request region as
// used. A new exact region starts at the closest unexpired ancestor's budget.
func (learner *adaptiveLearner) budget(request adaptiveRequest) int {
	learner.mu.Lock()
	defer learner.mu.Unlock()
	region := learner.regionForRequestLocked(request)
	return region.budget
}

// recordUncompacted persists last-use information without changing evidence.
func (learner *adaptiveLearner) recordUncompacted(request adaptiveRequest) {
	learner.mu.Lock()
	defer learner.mu.Unlock()
	learner.regionForRequestLocked(request)
	learner.persistLocked()
}

// recordCompacted records one output that exceeded its current region budget.
// Eight such calls without an intervening matching full retry reduce the
// region by one step.
func (learner *adaptiveLearner) recordCompacted(request adaptiveRequest) {
	learner.mu.Lock()
	defer learner.mu.Unlock()
	region := learner.regionForRequestLocked(request)
	regionID := request.regionChain[0]
	region.quietCalls++
	if region.quietCalls >= adaptiveQuietLimit {
		region.budget = max(adaptiveDefaultBudget, region.budget-adaptiveBudgetStep)
		region.quietCalls = 0
	}
	learner.recent = append(learner.recent, adaptiveCompactedCall{
		region:      regionID,
		fingerprint: request.fingerprint,
	})
	if excess := len(learner.recent) - adaptiveRecentLimit; excess > 0 {
		copy(learner.recent, learner.recent[excess:])
		learner.recent = learner.recent[:adaptiveRecentLimit]
	}
	learner.persistLocked()
}

// recordFull treats a full call as insufficiency evidence only when it matches
// one not-yet-consumed compacted call in the last-three window. Two such
// retries for one region grow its budget and clear the evidence window.
func (learner *adaptiveLearner) recordFull(request adaptiveRequest) {
	learner.mu.Lock()
	defer learner.mu.Unlock()
	region := learner.regionForRequestLocked(request)
	regionID := request.regionChain[0]
	matched := -1
	for index := len(learner.recent) - 1; index >= 0; index-- {
		call := &learner.recent[index]
		if !call.retried && call.region == regionID &&
			call.fingerprint == request.fingerprint {
			matched = index
			break
		}
	}
	if matched >= 0 {
		learner.recent[matched].retried = true
		region.quietCalls = 0
		retries := 0
		for index := range learner.recent {
			if learner.recent[index].region == regionID && learner.recent[index].retried {
				retries++
			}
		}
		if retries >= 2 {
			region.budget = min(adaptiveMaximumBudget, region.budget+adaptiveBudgetStep)
			learner.recent = nil
		}
	}
	learner.persistLocked()
}

func (learner *adaptiveLearner) regionForRequestLocked(
	request adaptiveRequest,
) *adaptiveRegion {
	now := learner.now()
	learner.expireLocked(now)
	if len(request.regionChain) == 0 {
		request.regionChain = []string{adaptiveRegionID(".")}
	}
	regionID := request.regionChain[0]
	if region, ok := learner.regions[regionID]; ok {
		region.lastUsed = now
		return region
	}
	budget := adaptiveDefaultBudget
	for _, ancestor := range request.regionChain[1:] {
		if inherited, ok := learner.regions[ancestor]; ok {
			budget = inherited.budget
			break
		}
	}
	region := &adaptiveRegion{budget: budget, lastUsed: now}
	learner.regions[regionID] = region
	learner.pruneLRULocked()
	return region
}

func (learner *adaptiveLearner) expireLocked(now time.Time) {
	expired := make(map[string]struct{})
	for id, region := range learner.regions {
		age := now.Sub(region.lastUsed)
		if age >= adaptiveRegionExpiry {
			delete(learner.regions, id)
			expired[id] = struct{}{}
		}
	}
	learner.removeRecentRegionsLocked(expired)
}

func (learner *adaptiveLearner) pruneLRULocked() {
	for len(learner.regions) > adaptiveRegionLimit {
		var oldestID string
		var oldest time.Time
		for id, region := range learner.regions {
			if oldestID == "" || region.lastUsed.Before(oldest) ||
				(region.lastUsed.Equal(oldest) && id < oldestID) {
				oldestID = id
				oldest = region.lastUsed
			}
		}
		delete(learner.regions, oldestID)
		learner.removeRecentRegionsLocked(map[string]struct{}{oldestID: {}})
	}
}

func (learner *adaptiveLearner) removeRecentRegionsLocked(regions map[string]struct{}) {
	if len(regions) == 0 || len(learner.recent) == 0 {
		return
	}
	retained := learner.recent[:0]
	for _, call := range learner.recent {
		if _, remove := regions[call.region]; !remove {
			retained = append(retained, call)
		}
	}
	learner.recent = retained
}

func (learner *adaptiveLearner) loadState() {
	info, err := os.Lstat(learner.statePath)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return
	}
	if err != nil {
		return
	}
	file, err := os.Open(learner.statePath)
	if err != nil {
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(file, adaptiveStateMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > adaptiveStateMaxBytes {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state adaptivePersistentState
	if err := decoder.Decode(&state); err != nil || state.Version != adaptiveStateVersion {
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return
	}
	if len(state.Regions) > adaptiveRegionLimit {
		return
	}
	now := learner.now()
	loaded := make(map[string]*adaptiveRegion, min(len(state.Regions), adaptiveRegionLimit))
	for _, persisted := range state.Regions {
		if !validRegionID(persisted.ID) ||
			persisted.Budget < adaptiveDefaultBudget ||
			persisted.Budget > adaptiveMaximumBudget ||
			(persisted.Budget-adaptiveDefaultBudget)%adaptiveBudgetStep != 0 ||
			persisted.QuietCalls < 0 || persisted.QuietCalls >= adaptiveQuietLimit ||
			persisted.LastUsed <= 0 || persisted.LastUsed > now.Unix() {
			learner.regions = make(map[string]*adaptiveRegion)
			return
		}
		if _, duplicate := loaded[persisted.ID]; duplicate {
			learner.regions = make(map[string]*adaptiveRegion)
			return
		}
		loaded[persisted.ID] = &adaptiveRegion{
			budget:     persisted.Budget,
			quietCalls: persisted.QuietCalls,
			lastUsed:   time.Unix(persisted.LastUsed, 0),
		}
	}
	learner.regions = loaded
	learner.expireLocked(now)
	learner.pruneLRULocked()
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (learner *adaptiveLearner) persistLocked() {
	if learner.stateWriter == nil {
		return
	}
	learner.expireLocked(learner.now())
	learner.pruneLRULocked()
	state := adaptivePersistentState{
		Version: adaptiveStateVersion,
		Regions: make([]adaptivePersistentRegion, 0, len(learner.regions)),
	}
	for id, region := range learner.regions {
		state.Regions = append(state.Regions, adaptivePersistentRegion{
			ID:         id,
			Budget:     region.budget,
			QuietCalls: region.quietCalls,
			LastUsed:   region.lastUsed.Unix(),
		})
	}
	sort.Slice(state.Regions, func(left, right int) bool {
		return state.Regions[left].ID < state.Regions[right].ID
	})
	if err := learner.stateWriter(state); err != nil {
		learner.logFailure.Do(func() {
			learner.logger.Warn("cannot persist MCP adaptive-output learning; continuing with session state", "error", err)
		})
	}
}

func (learner *adaptiveLearner) writeStateAtomic(state adaptivePersistentState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	directory := filepath.Dir(learner.statePath)
	temporary, err := os.CreateTemp(directory, ".adaptive-output-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, learner.statePath); err != nil {
		return err
	}
	committed = true
	return nil
}

func repositoryCacheKey(repositoryRoot string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(repositoryRoot)))
	return hex.EncodeToString(digest[:])
}

func validRegionID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func pathInsideRepository(repositoryRoot, candidate string) (bool, error) {
	return pathContainedBy(repositoryRoot, candidate)
}

func pathContainedBy(parent, candidate string) (bool, error) {
	root, err := resolveExistingPath(parent)
	if err != nil {
		return false, err
	}
	resolvedCandidate, err := resolveExistingPath(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, resolvedCandidate)
	if err != nil {
		return false, err
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

// resolveExistingPath resolves symlinks in the longest existing ancestor and
// then appends any not-yet-created suffix. It lets cache containment checks run
// before creating a cache directory.
func resolveExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for index := len(suffix) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, suffix[index])
	}
	return filepath.Clean(resolved), nil
}

func newAdaptiveRequest(tool string, input any) (adaptiveRequest, error) {
	var normalized any
	var region string
	switch value := input.(type) {
	case changedInput:
		normalized = normalizeChangedInput(value)
		region = regionFromPathGlobs(value.PathGlobs)
	case *changedInput:
		if value == nil {
			return adaptiveRequest{}, errors.New("nil changed input")
		}
		normalized = normalizeChangedInput(*value)
		region = regionFromPathGlobs(value.PathGlobs)
	case findInput:
		normalized = normalizeFindInput(value)
		region = regionFromPathGlobs(value.PathGlobs)
	case *findInput:
		if value == nil {
			return adaptiveRequest{}, errors.New("nil find input")
		}
		normalized = normalizeFindInput(*value)
		region = regionFromPathGlobs(value.PathGlobs)
	case inspectInput:
		normalized = normalizeInspectInput(value)
		region = regionFromLocation(value.Location)
	case *inspectInput:
		if value == nil {
			return adaptiveRequest{}, errors.New("nil inspect input")
		}
		normalized = normalizeInspectInput(*value)
		region = regionFromLocation(value.Location)
	case outlineInput:
		normalized = normalizeOutlineInput(value)
		region = normalizeRegion(value.Path)
	case *outlineInput:
		if value == nil {
			return adaptiveRequest{}, errors.New("nil outline input")
		}
		normalized = normalizeOutlineInput(*value)
		region = normalizeRegion(value.Path)
	default:
		return adaptiveRequest{}, fmt.Errorf("unsupported adaptive input %T", input)
	}
	encoded, err := json.Marshal(struct {
		Arguments any    `json:"arguments"`
		Tool      string `json:"tool"`
	}{Arguments: normalized, Tool: tool})
	if err != nil {
		return adaptiveRequest{}, fmt.Errorf("fingerprint adaptive request: %w", err)
	}
	return adaptiveRequest{
		regionChain: adaptiveRegionChain(region),
		fingerprint: sha256.Sum256(encoded),
	}, nil
}

type normalizedCommonInput struct {
	Return         string   `json:"return"`
	PathGlobs      []string `json:"path_globs"`
	ExcludeGlobs   []string `json:"exclude_globs"`
	Context        int      `json:"context"`
	Limit          int      `json:"limit"`
	MaxCodeLines   int      `json:"max_code_lines"`
	DropComments   bool     `json:"drop_comments"`
	DropDocstrings bool     `json:"drop_docstrings"`
}

type normalizedChangedInput struct {
	normalizedCommonInput
	MaxPatchLines int `json:"max_patch_lines"`
}

type normalizedFindInput struct {
	Query   string `json:"query"`
	Match   string `json:"match"`
	Include string `json:"include"`
	normalizedCommonInput
	ChangedOnly     bool `json:"changed_only"`
	IncludeComments bool `json:"include_comments"`
	IncludeStrings  bool `json:"include_strings"`
}

type normalizedInspectInput struct {
	Location string `json:"location"`
	Include  string `json:"include"`
	normalizedCommonInput
	ChangedOnly     bool `json:"changed_only"`
	IncludeComments bool `json:"include_comments"`
	IncludeStrings  bool `json:"include_strings"`
}

type normalizedOutlineInput struct {
	Path           string `json:"path"`
	Return         string `json:"return"`
	Limit          int    `json:"limit"`
	MaxCodeLines   int    `json:"max_code_lines"`
	DropComments   bool   `json:"drop_comments"`
	DropDocstrings bool   `json:"drop_docstrings"`
}

func normalizeChangedInput(input changedInput) normalizedChangedInput {
	return normalizedChangedInput{
		normalizedCommonInput: normalizeCommonInput(input.commonInput, "context"),
		MaxPatchLines:         defaultInt(input.MaxPatchLines, defaultMaxPatchLines),
	}
}

func normalizeFindInput(input findInput) normalizedFindInput {
	return normalizedFindInput{
		normalizedCommonInput: normalizeCommonInput(input.commonInput, "locations"),
		Query:                 input.Query,
		Match:                 defaultString(input.Match, "auto"),
		Include:               defaultString(input.Include, "both"),
		ChangedOnly:           input.ChangedOnly,
		IncludeComments:       input.IncludeComments,
		IncludeStrings:        input.IncludeStrings,
	}
}

func normalizeInspectInput(input inspectInput) normalizedInspectInput {
	return normalizedInspectInput{
		normalizedCommonInput: normalizeCommonInput(input.commonInput, "scope"),
		Location:              input.Location,
		Include:               defaultString(input.Include, "scope"),
		ChangedOnly:           input.ChangedOnly,
		IncludeComments:       input.IncludeComments,
		IncludeStrings:        input.IncludeStrings,
	}
}

func normalizeOutlineInput(input outlineInput) normalizedOutlineInput {
	return normalizedOutlineInput{
		Path:           input.Path,
		Return:         defaultString(input.Return, "line"),
		Limit:          defaultInt(input.Limit, defaultLimit),
		MaxCodeLines:   defaultInt(input.MaxCodeLines, defaultMaxCodeLines),
		DropComments:   input.DropComments,
		DropDocstrings: input.DropDocstrings,
	}
}

func normalizeCommonInput(input commonInput, defaultReturn string) normalizedCommonInput {
	return normalizedCommonInput{
		Return:         defaultString(input.Return, defaultReturn),
		PathGlobs:      normalizeStringSet(input.PathGlobs),
		ExcludeGlobs:   normalizeStringSet(input.ExcludeGlobs),
		Context:        defaultInt(input.Context, defaultContext),
		Limit:          defaultInt(input.Limit, defaultLimit),
		MaxCodeLines:   defaultInt(input.MaxCodeLines, defaultMaxCodeLines),
		DropComments:   input.DropComments,
		DropDocstrings: input.DropDocstrings,
	}
}

func normalizeStringSet(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	normalized := append([]string(nil), values...)
	sort.Strings(normalized)
	write := 0
	for _, value := range normalized {
		if write != 0 && normalized[write-1] == value {
			continue
		}
		normalized[write] = value
		write++
	}
	return normalized[:write]
}

func regionFromLocation(location string) string {
	separator := strings.LastIndexByte(location, ':')
	if separator <= 0 {
		return "."
	}
	return normalizeRegion(location[:separator])
}

func regionFromPathGlobs(globs []string) string {
	best := "."
	bestLength := 0
	for _, glob := range globs {
		candidate := literalDirectoryPrefix(glob)
		if candidate == "." {
			continue
		}
		if len(candidate) > bestLength ||
			(len(candidate) == bestLength && candidate < best) {
			best = candidate
			bestLength = len(candidate)
		}
	}
	return best
}

func literalDirectoryPrefix(glob string) string {
	pattern := filepath.ToSlash(glob)
	for strings.HasPrefix(pattern, "./") {
		pattern = strings.TrimPrefix(pattern, "./")
	}
	if pattern == "" || strings.HasPrefix(pattern, "/") {
		return "."
	}
	meta := firstGlobMeta(pattern)
	literal := pattern
	if meta >= 0 {
		literal = pattern[:meta]
	}
	if meta < 0 && strings.HasSuffix(literal, "/") {
		return normalizeRegion(strings.TrimSuffix(literal, "/"))
	}
	separator := strings.LastIndexByte(literal, '/')
	if separator < 0 {
		return "."
	}
	return normalizeRegion(strings.TrimSuffix(literal[:separator+1], "/"))
}

func firstGlobMeta(pattern string) int {
	escaped := false
	for index, value := range pattern {
		if escaped {
			escaped = false
			continue
		}
		if value == '\\' {
			escaped = true
			continue
		}
		if value == '*' || value == '?' || value == '[' {
			return index
		}
	}
	return -1
}

func normalizeRegion(region string) string {
	region = filepath.ToSlash(region)
	for strings.HasPrefix(region, "./") {
		region = strings.TrimPrefix(region, "./")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(region)))
	if cleaned == "" || cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "."
	}
	return cleaned
}

func adaptiveRegionChain(region string) []string {
	region = normalizeRegion(region)
	regions := []string{region}
	for region != "." {
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(region)))
		if parent == "" {
			parent = "."
		}
		region = parent
		regions = append(regions, region)
	}
	ids := make([]string, len(regions))
	for index := range regions {
		ids[index] = adaptiveRegionID(regions[index])
	}
	return ids
}

func adaptiveRegionID(region string) string {
	digest := sha256.Sum256([]byte("scopesifter-mcp-region/v1\x00" + normalizeRegion(region)))
	return hex.EncodeToString(digest[:])
}
