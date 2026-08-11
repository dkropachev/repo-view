//go:build linux

package workspace

import (
	"bytes"
	"testing"
)

func TestValidateFrozenOverlayTransitionRequiresExactReadOnlyChange(t *testing.T) {
	t.Parallel()
	before := mountRecord{
		majorMinor: "0:42",
		root:       "/",
		point:      "/workspace/model",
		filesystem: "overlay",
		source:     "overlay",
		options:    []string{"rw", "nosuid", "nodev", "noatime"},
		optional:   []string{"private"},
		superOptions: []string{
			"rw", "lowerdir=/lower", "upperdir=/upper", "workdir=/work",
		},
		namespace: namespaceIdentity{device: 7, inode: 8},
		id:        42,
		parentID:  41,
	}
	after := before
	after.options = []string{"ro", "nosuid", "nodev", "noatime"}
	if err := validateFrozenOverlayTransition(before, after); err != nil {
		t.Fatal(err)
	}
	relocated := after
	relocated.point = "/workspace-renamed/model"
	relocated.parentID++
	if err := validateFrozenOverlayTransition(before, relocated); err != nil {
		t.Fatalf("attachment-only relocation changed frozen mount identity: %v", err)
	}

	tests := map[string]func(*mountRecord){
		"mount identity": func(record *mountRecord) { record.id++ },
		"still writable": func(record *mountRecord) {
			record.options = []string{"rw", "nosuid", "nodev", "noatime"}
		},
		"missing restriction": func(record *mountRecord) {
			record.options = []string{"ro", "nosuid", "noatime"}
		},
		"extra option": func(record *mountRecord) {
			record.options = []string{"ro", "nosuid", "nodev", "noatime", "relatime"}
		},
		"reordered options": func(record *mountRecord) {
			record.options = []string{"ro", "nodev", "nosuid", "noatime"}
		},
		"non-executable": func(record *mountRecord) {
			record.options = []string{"ro", "nosuid", "nodev", "noatime", "noexec"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutated := after
			mutate(&mutated)
			if err := validateFrozenOverlayTransition(before, mutated); err == nil {
				t.Fatal("invalid frozen-overlay transition was accepted")
			}
		})
	}
}

func TestVerifiedFreezeRecordIsNeverRefreshedDuringCleanup(t *testing.T) {
	t.Parallel()
	arm := &ArmAuthority{
		frozen:         true,
		freezeVerified: true,
		paths:          ArmPaths{ModelRoot: "/definitely/absent/tokenbench-workspace"},
	}
	if err := arm.refreshFrozenOverlayMount(); err != nil {
		t.Fatalf("verified frozen record was unexpectedly refreshed: %v", err)
	}
}

func TestCloneOutcomePreservesPatchPresenceAndOwnership(t *testing.T) {
	t.Parallel()
	if cloneOutcome(Outcome{}).Patch != nil {
		t.Fatal("nil patch became present")
	}
	empty := cloneOutcome(Outcome{Patch: []byte{}})
	if empty.Patch == nil || len(empty.Patch) != 0 {
		t.Fatal("present empty patch lost its identity")
	}
	source := Outcome{Patch: []byte("patch")}
	clone := cloneOutcome(source)
	clone.Patch[0] = 'P'
	if !bytes.Equal(source.Patch, []byte("patch")) || !bytes.Equal(clone.Patch, []byte("Patch")) {
		t.Fatal("outcome patch was not deeply cloned")
	}
}
