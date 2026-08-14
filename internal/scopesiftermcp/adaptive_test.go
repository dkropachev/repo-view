package scopesiftermcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdaptiveRequestNormalizesDefaultsAndSetArguments(t *testing.T) {
	implicit := mustAdaptiveRequest(t, "find", findInput{Query: "Target"})
	explicit := mustAdaptiveRequest(t, "find", findInput{
		Query:   "Target",
		Match:   "auto",
		Include: "both",
		commonInput: commonInput{
			Response:     "full",
			Return:       "locations",
			PathGlobs:    []string{},
			ExcludeGlobs: []string{},
			Context:      defaultContext,
			Limit:        defaultLimit,
			MaxCodeLines: defaultMaxCodeLines,
		},
	})
	if implicit.fingerprint != explicit.fingerprint {
		t.Fatal("explicit defaults or response mode changed the normalized fingerprint")
	}

	reordered := mustAdaptiveRequest(t, "find", findInput{
		Query: "Target",
		commonInput: commonInput{
			PathGlobs:    []string{"cmd/**", "internal/**", "cmd/**"},
			ExcludeGlobs: []string{"vendor/**", "testdata/**"},
		},
	})
	canonical := mustAdaptiveRequest(t, "find", findInput{
		Query: "Target",
		commonInput: commonInput{
			PathGlobs:    []string{"internal/**", "cmd/**"},
			ExcludeGlobs: []string{"testdata/**", "vendor/**"},
		},
	})
	if reordered.fingerprint != canonical.fingerprint {
		t.Fatal("semantically identical path-filter sets produced different fingerprints")
	}

	otherTool := mustAdaptiveRequest(t, "inspect", inspectInput{Location: "Target:1"})
	if implicit.fingerprint == otherTool.fingerprint {
		t.Fatal("tool name was omitted from adaptive fingerprint")
	}
}

func TestAdaptiveRegionSelection(t *testing.T) {
	tests := []struct {
		name  string
		globs []string
		want  string
	}{
		{name: "repository", want: "."},
		{name: "basename wildcard", globs: []string{"*.go"}, want: "."},
		{name: "recursive directory", globs: []string{"internal/scopesiftermcp/**"}, want: "internal/scopesiftermcp"},
		{name: "filename wildcard", globs: []string{"internal/scopesiftermcp/*.go"}, want: "internal/scopesiftermcp"},
		{name: "literal filename or substring", globs: []string{"internal/server.go"}, want: "internal"},
		{name: "literal directory slash", globs: []string{"internal/scopesiftermcp/"}, want: "internal/scopesiftermcp"},
		{name: "longest", globs: []string{"cmd/**", "internal/scopesiftermcp/*.go", "navigator/**"}, want: "internal/scopesiftermcp"},
		{name: "unsafe parent", globs: []string{"../private/**"}, want: "."},
		{name: "leading wildcard", globs: []string{"**/scopesiftermcp/**"}, want: "."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := regionFromPathGlobs(test.globs); got != test.want {
				t.Fatalf("regionFromPathGlobs(%q) = %q, want %q", test.globs, got, test.want)
			}
		})
	}

	inspect := mustAdaptiveRequest(t, "inspect", inspectInput{Location: "internal/scopesiftermcp/server.go:42"})
	outline := mustAdaptiveRequest(t, "outline", outlineInput{Path: "internal/scopesiftermcp/server.go"})
	wantChain := adaptiveRegionChain("internal/scopesiftermcp/server.go")
	if !reflect.DeepEqual(inspect.regionChain, wantChain) ||
		!reflect.DeepEqual(outline.regionChain, wantChain) {
		t.Fatalf("file region chains = %q and %q, want %q", inspect.regionChain, outline.regionChain, wantChain)
	}

	leftFirst := regionFromPathGlobs([]string{"beta/pkg/**", "alpha/pkg/**"})
	rightFirst := regionFromPathGlobs([]string{"alpha/pkg/**", "beta/pkg/**"})
	if leftFirst != rightFirst || leftFirst != "alpha/pkg" {
		t.Fatalf("equal-length regions depend on glob order: %q and %q", leftFirst, rightFirst)
	}
}

func TestAdaptiveLearnerNeedsTwoDistinctMatchingRetries(t *testing.T) {
	learner := newSessionLearner(t, t.TempDir(), time.Now)
	request := mustAdaptiveRequest(t, "find", findInput{
		Query:       "Target",
		commonInput: commonInput{PathGlobs: []string{"internal/pkg/**"}},
	})
	if got := learner.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("initial budget = %d", got)
	}

	learner.recordCompacted(request)
	learner.recordFull(request)
	learner.recordFull(request)
	if got := learner.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("one compacted call consumed twice grew budget to %d", got)
	}

	learner.recordCompacted(request)
	learner.recordFull(request)
	if got := learner.budget(request); got != adaptiveDefaultBudget+adaptiveBudgetStep {
		t.Fatalf("two matching retries budget = %d", got)
	}
	if len(learner.recent) != 0 {
		t.Fatalf("growth window was not cleared: %#v", learner.recent)
	}
}

func TestAdaptiveLearnerUsesOnlyLastThreeCompactedCalls(t *testing.T) {
	learner := newSessionLearner(t, t.TempDir(), time.Now)
	requests := make([]adaptiveRequest, 4)
	for index := range requests {
		requests[index] = mustAdaptiveRequest(t, "find", findInput{
			Query:       fmt.Sprintf("Target%d", index),
			commonInput: commonInput{PathGlobs: []string{"internal/pkg/**"}},
		})
		learner.recordCompacted(requests[index])
	}
	learner.recordFull(requests[0])
	learner.recordFull(requests[1])
	if got := learner.budget(requests[0]); got != adaptiveDefaultBudget {
		t.Fatalf("retry outside last-three window grew budget to %d", got)
	}
	learner.recordFull(requests[2])
	if got := learner.budget(requests[0]); got != adaptiveDefaultBudget+adaptiveBudgetStep {
		t.Fatalf("two retries inside last-three window budget = %d", got)
	}
}

func TestAdaptiveLearnerIgnoresUnrelatedFullAndProgressiveCalls(t *testing.T) {
	learner := newSessionLearner(t, t.TempDir(), time.Now)
	broad := mustAdaptiveRequest(t, "find", findInput{Query: "Target"})
	unrelated := mustAdaptiveRequest(t, "find", findInput{Query: "Other"})
	inspect := mustAdaptiveRequest(t, "inspect", inspectInput{Location: "internal/pkg/file.go:20"})
	learner.recordCompacted(broad)
	learner.recordFull(unrelated)
	learner.recordFull(inspect)

	region := learner.regions[broad.regionChain[0]]
	if region.quietCalls != 1 || region.budget != adaptiveDefaultBudget {
		t.Fatalf("unrelated full call changed broad region: %#v", region)
	}
	learner.recordCompacted(broad)
	learner.recordFull(broad)
	if got := learner.budget(broad); got != adaptiveDefaultBudget {
		t.Fatalf("one matching retry after progressive calls budget = %d", got)
	}
}

func TestAdaptiveLearnerCeilingAndQuietDecay(t *testing.T) {
	learner := newSessionLearner(t, t.TempDir(), time.Now)
	request := mustAdaptiveRequest(t, "changed", changedInput{})
	for learner.budget(request) < adaptiveMaximumBudget {
		growAdaptiveBudget(learner, request)
	}
	if got := learner.budget(request); got != adaptiveMaximumBudget {
		t.Fatalf("maximum budget = %d", got)
	}
	growAdaptiveBudget(learner, request)
	if got := learner.budget(request); got != adaptiveMaximumBudget {
		t.Fatalf("budget exceeded maximum: %d", got)
	}

	for range adaptiveQuietLimit - 1 {
		learner.recordCompacted(request)
	}
	if got := learner.budget(request); got != adaptiveMaximumBudget {
		t.Fatalf("budget decayed before eight quiet calls: %d", got)
	}
	learner.recordCompacted(request)
	if got := learner.budget(request); got != adaptiveMaximumBudget-adaptiveBudgetStep {
		t.Fatalf("budget after quiet decay = %d", got)
	}
	if quiet := learner.regions[request.regionChain[0]].quietCalls; quiet != 0 {
		t.Fatalf("quiet count after decay = %d", quiet)
	}

	learner.recordCompacted(request)
	learner.recordFull(request)
	if quiet := learner.regions[request.regionChain[0]].quietCalls; quiet != 0 {
		t.Fatalf("matching retry did not reset quiet count: %d", quiet)
	}
}

func TestAdaptiveLearnerInheritsClosestAncestorAndExpires(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clock := func() time.Time { return now }
	learner := newSessionLearner(t, t.TempDir(), clock)
	root := mustAdaptiveRequest(t, "changed", changedInput{})
	directory := mustAdaptiveRequest(t, "find", findInput{
		Query:       "Target",
		commonInput: commonInput{PathGlobs: []string{"internal/pkg/**"}},
	})
	file := mustAdaptiveRequest(t, "inspect", inspectInput{Location: "internal/pkg/file.go:10"})
	sibling := mustAdaptiveRequest(t, "outline", outlineInput{Path: "cmd/tool/main.go"})

	growAdaptiveBudget(learner, root)
	growAdaptiveBudget(learner, directory)
	if got := learner.budget(directory); got != adaptiveDefaultBudget+2*adaptiveBudgetStep {
		t.Fatalf("directory inherited root then grew to %d", got)
	}
	if got := learner.budget(file); got != adaptiveDefaultBudget+2*adaptiveBudgetStep {
		t.Fatalf("file did not inherit closest directory: %d", got)
	}
	if got := learner.budget(sibling); got != adaptiveDefaultBudget+adaptiveBudgetStep {
		t.Fatalf("sibling did not inherit repository region: %d", got)
	}

	now = now.Add(adaptiveRegionExpiry)
	newFile := mustAdaptiveRequest(t, "outline", outlineInput{Path: "internal/pkg/new.go"})
	if got := learner.budget(newFile); got != adaptiveDefaultBudget {
		t.Fatalf("expired ancestor budget = %d", got)
	}
	if len(learner.recent) != 0 {
		t.Fatalf("expiry retained retry evidence: %#v", learner.recent)
	}
}

func TestAdaptivePersistenceHashesDataAndRestoresBudget(t *testing.T) {
	repository := t.TempDir()
	cacheBase := t.TempDir()
	now := time.Unix(2_000_000_000, 0)
	options := adaptiveLearnerOptions{
		persistent: true,
		cacheBase:  cacheBase,
		now:        func() time.Time { return now },
	}
	learner, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	request := mustAdaptiveRequest(t, "find", findInput{
		Query:       "SensitiveQuery",
		commonInput: commonInput{PathGlobs: []string{"private/source/**"}},
	})
	growAdaptiveBudget(learner, request)
	for range 3 {
		learner.recordCompacted(request)
	}

	wantPath := filepath.Join(
		cacheBase,
		ImplementationName,
		adaptiveCacheDirectory,
		repositoryCacheKey(repository)+".json",
	)
	if learner.statePath != wantPath {
		t.Fatalf("state path = %q, want %q", learner.statePath, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{repository, "private", "source", "SensitiveQuery", "fingerprint"} {
		if bytes.Contains(data, []byte(plaintext)) {
			t.Fatalf("persistent state contains %q: %s", plaintext, data)
		}
	}
	var state adaptivePersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != adaptiveStateVersion || len(state.Regions) != 1 ||
		state.Regions[0].ID != request.regionChain[0] ||
		state.Regions[0].Budget != adaptiveDefaultBudget+adaptiveBudgetStep ||
		state.Regions[0].QuietCalls != 3 {
		t.Fatalf("persistent state = %#v", state)
	}
	assertFileMode(t, filepath.Dir(wantPath), 0o700)
	assertFileMode(t, wantPath, 0o600)

	restored, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.budget(request); got != adaptiveDefaultBudget+adaptiveBudgetStep {
		t.Fatalf("restored budget = %d", got)
	}
	if len(restored.recent) != 0 {
		t.Fatalf("session retry evidence was persisted: %#v", restored.recent)
	}
	for range adaptiveQuietLimit - 3 {
		restored.recordCompacted(request)
	}
	if got := restored.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("restored quiet count did not continue decay: %d", got)
	}
	child := mustAdaptiveRequest(t, "inspect", inspectInput{Location: "private/source/file.go:7"})
	if got := restored.budget(child); got != adaptiveDefaultBudget {
		t.Fatalf("child did not inherit restored directory budget: %d", got)
	}
	now = now.Add(adaptiveRegionExpiry)
	expired, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := expired.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("expired persistent budget = %d", got)
	}
}

func TestAdaptivePersistenceCorruptionResetsSafely(t *testing.T) {
	repository := t.TempDir()
	cacheBase := t.TempDir()
	options := adaptiveLearnerOptions{persistent: true, cacheBase: cacheBase}
	learner, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(learner.statePath, []byte(`{"version":1,"regions":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatalf("corrupt cache prevented startup: %v", err)
	}
	request := mustAdaptiveRequest(t, "changed", changedInput{})
	if got := restored.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("budget from corrupt state = %d", got)
	}
	restored.recordCompacted(request)
	data, err := os.ReadFile(restored.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state adaptivePersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("cache was not repaired: %v\n%s", err, data)
	}
}

func TestAdaptivePersistenceRejectsFutureTimestamp(t *testing.T) {
	repository := t.TempDir()
	now := time.Unix(2_000_000_000, 0)
	options := adaptiveLearnerOptions{
		persistent: true,
		cacheBase:  t.TempDir(),
		now:        func() time.Time { return now },
	}
	learner, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	request := mustAdaptiveRequest(t, "changed", changedInput{})
	state := adaptivePersistentState{
		Version: adaptiveStateVersion,
		Regions: []adaptivePersistentRegion{{
			ID: request.regionChain[0], Budget: adaptiveDefaultBudget + adaptiveBudgetStep,
			LastUsed: now.Add(time.Hour).Unix(),
		}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(learner.statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := newAdaptiveLearnerWithOptions(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.budget(request); got != adaptiveDefaultBudget {
		t.Fatalf("future cache timestamp restored budget %d", got)
	}
}

func TestAdaptivePersistenceRejectsRepositoryPathWithoutWriting(t *testing.T) {
	repository := t.TempDir()
	_, err := newAdaptiveLearnerWithOptions(repository, adaptiveLearnerOptions{
		persistent: true,
		cacheBase:  filepath.Join(repository, "cache"),
	})
	if err == nil || !strings.Contains(err.Error(), "must not be inside") {
		t.Fatalf("error = %v", err)
	}
	entries, readErr := os.ReadDir(repository)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected cache path wrote repository entries: %#v", entries)
	}

	// Session-only learning does not inspect or write any cache path.
	learner, err := newAdaptiveLearnerWithOptions(repository, adaptiveLearnerOptions{
		cacheBase: filepath.Join(repository, "ignored"),
	})
	if err != nil {
		t.Fatal(err)
	}
	learner.recordCompacted(mustAdaptiveRequest(t, "changed", changedInput{}))
	entries, err = os.ReadDir(repository)
	if err != nil || len(entries) != 0 {
		t.Fatalf("session learner changed repository: entries=%#v error=%v", entries, err)
	}
}

func TestAdaptivePersistenceWriteFailureLogsOnceAndKeepsLearning(t *testing.T) {
	repository := t.TempDir()
	cacheBase := t.TempDir()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	learner, err := newAdaptiveLearnerWithOptions(repository, adaptiveLearnerOptions{
		persistent: true,
		cacheBase:  cacheBase,
		logger:     logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	learner.stateWriter = func(adaptivePersistentState) error {
		return errors.New("disk unavailable")
	}
	request := mustAdaptiveRequest(t, "changed", changedInput{})
	growAdaptiveBudget(learner, request)
	learner.recordCompacted(request)
	if got := learner.budget(request); got != adaptiveDefaultBudget+adaptiveBudgetStep {
		t.Fatalf("write failure discarded session budget: %d", got)
	}
	if count := strings.Count(logs.String(), "cannot persist MCP adaptive-output learning"); count != 1 {
		t.Fatalf("write failure log count = %d\n%s", count, logs.String())
	}
}

func TestAdaptiveLearnerCapsRegionsByLRU(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	learner := newSessionLearner(t, t.TempDir(), func() time.Time { return now })
	var first, last adaptiveRequest
	for index := 0; index <= adaptiveRegionLimit; index++ {
		request := mustAdaptiveRequest(t, "outline", outlineInput{
			Path: fmt.Sprintf("region-%03d/file.go", index),
		})
		if index == 0 {
			first = request
		}
		last = request
		learner.budget(request)
		now = now.Add(time.Second)
	}
	if len(learner.regions) != adaptiveRegionLimit {
		t.Fatalf("region count = %d", len(learner.regions))
	}
	if _, ok := learner.regions[first.regionChain[0]]; ok {
		t.Fatal("oldest region was not evicted")
	}
	if _, ok := learner.regions[last.regionChain[0]]; !ok {
		t.Fatal("newest region was evicted")
	}
}

func TestAdaptivePersistenceCapsSerializedRegionsByLRU(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	learner, err := newAdaptiveLearnerWithOptions(t.TempDir(), adaptiveLearnerOptions{
		persistent: true,
		cacheBase:  t.TempDir(),
		now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	var first, last adaptiveRequest
	for index := 0; index <= adaptiveRegionLimit; index++ {
		request := mustAdaptiveRequest(t, "outline", outlineInput{
			Path: fmt.Sprintf("persistent-region-%03d/file.go", index),
		})
		if index == 0 {
			first = request
		}
		last = request
		learner.budget(request)
		now = now.Add(time.Second)
	}
	learner.recordUncompacted(last)
	data, err := os.ReadFile(learner.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state adaptivePersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Regions) != adaptiveRegionLimit {
		t.Fatalf("serialized region count = %d", len(state.Regions))
	}
	for _, region := range state.Regions {
		if region.ID == first.regionChain[0] {
			t.Fatal("serialized state retained oldest region")
		}
	}
}

func mustAdaptiveRequest(t *testing.T, tool string, input any) adaptiveRequest {
	t.Helper()
	request, err := newAdaptiveRequest(tool, input)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func newSessionLearner(
	t *testing.T,
	repository string,
	clock func() time.Time,
) *adaptiveLearner {
	t.Helper()
	learner, err := newAdaptiveLearnerWithOptions(repository, adaptiveLearnerOptions{now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return learner
}

func growAdaptiveBudget(learner *adaptiveLearner, request adaptiveRequest) {
	learner.recordCompacted(request)
	learner.recordCompacted(request)
	learner.recordFull(request)
	learner.recordFull(request)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
