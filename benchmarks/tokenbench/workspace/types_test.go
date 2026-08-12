package workspace

import (
	"strconv"
	"strings"
	"testing"
)

func TestMountPolicyCommitsInfrastructureInodeAllowance(t *testing.T) {
	t.Parallel()
	want := "infrastructure-inodes=" + strconv.Itoa(workspaceInfrastructureInodes) + "\n"
	if !strings.Contains(mountPolicyDocument, want) {
		t.Fatalf("workspace mount policy omits %q", want)
	}
}

func TestInputsValidateCommitmentAndDisjointPaths(t *testing.T) {
	t.Parallel()
	inputs := validInputs()
	if err := inputs.Validate(); err != nil {
		t.Fatal(err)
	}
	mutated := inputs
	mutated.Limits.MaximumEntries++
	if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "commitment") {
		t.Fatalf("mutated committed input was accepted: %v", err)
	}
	for _, lower := range []string{"/workspace/model", "/workspace/model/lower"} {
		mutated = inputs
		mutated.ImmutableLowerRoot = lower
		mutated.Commitment = mutated.ComputeCommitment()
		if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "disjoint") {
			t.Fatalf("overlapping lower path %q was accepted: %v", lower, err)
		}
	}
	mutated = inputs
	mutated.ImmutableLowerRoot = "/immutable/lower"
	mutated.Commitment = mutated.ComputeCommitment()
	if err := mutated.Validate(); err != nil {
		t.Fatalf("disjoint sibling paths were rejected: %v", err)
	}
}

func TestInputsRejectInvalidAuditFields(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Inputs){
		"schema":              func(inputs *Inputs) { inputs.SchemaVersion = "other" },
		"relative model root": func(inputs *Inputs) { inputs.ModelRoot = "workspace" },
		"root lower":          func(inputs *Inputs) { inputs.ImmutableLowerRoot = "/" },
		"noncanonical lower":  func(inputs *Inputs) { inputs.ImmutableLowerRoot = "/immutable/../lower" },
		"nul model root":      func(inputs *Inputs) { inputs.ModelRoot = "/workspace/\x00model" },
		"invalid UTF-8 lower": func(inputs *Inputs) { inputs.ImmutableLowerRoot = "/snapshot/\xfflower" },
		"long path":           func(inputs *Inputs) { inputs.ModelRoot = "/" + strings.Repeat("w", maximumPathBytes) },
		"base digest":         func(inputs *Inputs) { inputs.BaseTreeSHA256 = "bad" },
		"snapshot digest":     func(inputs *Inputs) { inputs.SnapshotCommitment = "bad" },
		"changed digest":      func(inputs *Inputs) { inputs.ChangedStateSHA256 = "bad" },
		"mount digest":        func(inputs *Inputs) { inputs.MountPolicySHA256 = "bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inputs := validInputs()
			mutate(&inputs)
			inputs.Commitment = inputs.ComputeCommitment()
			if err := inputs.Validate(); err == nil {
				t.Fatal("invalid workspace inputs were accepted")
			}
		})
	}
}

func TestLimitsRejectUnboundedOrInconsistentValues(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Limits){
		"upper zero":           func(limits *Limits) { limits.MaximumUpperBytes = 0 },
		"upper large":          func(limits *Limits) { limits.MaximumUpperBytes = maximumUpperBytes + 1 },
		"entries zero":         func(limits *Limits) { limits.MaximumEntries = 0 },
		"entries large":        func(limits *Limits) { limits.MaximumEntries = maximumEntries + 1 },
		"file over upper":      func(limits *Limits) { limits.MaximumFileBytes = limits.MaximumUpperBytes + 1 },
		"patch large":          func(limits *Limits) { limits.MaximumPatchBytes = maximumPatchBytes + 1 },
		"changes over entries": func(limits *Limits) { limits.MaximumChangedFiles = limits.MaximumEntries + 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			limits := validLimits()
			mutate(&limits)
			if err := limits.Validate(); err == nil {
				t.Fatal("invalid workspace limits were accepted")
			}
		})
	}
}

func TestOutcomeCapturedBindsExactPatchAndBounds(t *testing.T) {
	t.Parallel()
	limits := validLimits()
	patch := []byte("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n")
	outcome := Outcome{
		SchemaVersion: OutcomeSchemaVersion, Status: StatusCaptured,
		InitialTreeSHA256:  digest([]byte("initial")),
		ResultTreeSHA256:   digest([]byte("result")),
		ResultTreeObjectID: strings.Repeat("1", 40),
		PatchSHA256:        digest(patch), Patch: patch,
		ChangedFiles: 1, ChangedLines: 2,
	}
	if err := outcome.Validate(limits); err != nil {
		t.Fatal(err)
	}
	mutated := outcome
	mutated.Patch = append([]byte(nil), patch...)
	mutated.Patch[len(mutated.Patch)-1] ^= 1
	if err := mutated.Validate(limits); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("mutated patch was accepted: %v", err)
	}
	mutated = outcome
	mutated.ChangedFiles = 0
	if err := mutated.Validate(limits); err == nil {
		t.Fatal("captured outcome without changed files was accepted")
	}
	mutated = outcome
	mutated.ResultTreeSHA256 = mutated.InitialTreeSHA256
	if err := mutated.Validate(limits); err == nil {
		t.Fatal("captured outcome with unchanged result was accepted")
	}
}

func TestOutcomeNoChangeAndFailureUnions(t *testing.T) {
	t.Parallel()
	limits := validLimits()
	initial := digest([]byte("initial"))
	noChange := Outcome{
		SchemaVersion: OutcomeSchemaVersion, Status: StatusNoChange,
		InitialTreeSHA256: initial, ResultTreeSHA256: initial,
		ResultTreeObjectID: strings.Repeat("2", 40), PatchSHA256: digest(nil),
	}
	if err := noChange.Validate(limits); err != nil {
		t.Fatal(err)
	}
	noChange.Patch = []byte("unexpected")
	if err := noChange.Validate(limits); err == nil {
		t.Fatal("no-change outcome with patch bytes was accepted")
	}

	for _, status := range []Status{StatusLimitExceeded, StatusInvalidTree} {
		failed := Outcome{
			SchemaVersion: OutcomeSchemaVersion, Status: status,
			InitialTreeSHA256: initial, ViolationCode: "patch_limit",
		}
		if err := failed.Validate(limits); err != nil {
			t.Fatalf("valid %s outcome: %v", status, err)
		}
		failed.ResultTreeSHA256 = digest([]byte("partial"))
		if err := failed.Validate(limits); err == nil {
			t.Fatalf("%s outcome with partial result was accepted", status)
		}
	}
}

func TestOutcomeRejectsUnknownStatusAndViolation(t *testing.T) {
	t.Parallel()
	limits := validLimits()
	unknown := Outcome{
		SchemaVersion: OutcomeSchemaVersion, Status: "other",
		InitialTreeSHA256: digest([]byte("initial")),
	}
	if err := unknown.Validate(limits); err == nil {
		t.Fatal("unknown workspace status was accepted")
	}
	unknown.Status = StatusInvalidTree
	unknown.ViolationCode = "Bad Code"
	if err := unknown.Validate(limits); err == nil {
		t.Fatal("noncanonical violation code was accepted")
	}
}

func validInputs() Inputs {
	inputs := Inputs{
		SchemaVersion: InputsSchemaVersion,
		ModelRoot:     "/workspace/model", ImmutableLowerRoot: "/snapshot/lower",
		BaseTreeSHA256:     digest([]byte("base")),
		SnapshotCommitment: digest([]byte("snapshot")),
		ChangedStateSHA256: digest([]byte("changed")),
		Limits:             validLimits(), MountPolicySHA256: requiredMountPolicySHA256,
	}
	inputs.Commitment = inputs.ComputeCommitment()
	return inputs
}

func validLimits() Limits {
	return Limits{
		MaximumUpperBytes: 1 << 30, MaximumEntries: 100_000,
		MaximumFileBytes: 64 << 20, MaximumPatchBytes: 16 << 20,
		MaximumChangedFiles: 10_000,
	}
}
