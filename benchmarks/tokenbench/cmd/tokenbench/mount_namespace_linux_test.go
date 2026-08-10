//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRewriteCredentialDescriptorIsExact(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "separate value",
			args: []string{"--suite", "/suite", "--credential-fd", "17", "--repetition", "0"},
			want: []string{"--suite", "/suite", "--credential-fd", "3", "--repetition", "0"},
		},
		{
			name: "equals value",
			args: []string{"--credential-fd=17", "--suite", "/suite"},
			want: []string{"--credential-fd=3", "--suite", "/suite"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rewriteCredentialDescriptor(test.args, 3)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("rewrite = %q, %v; want %q", got, err, test.want)
			}
			if reflect.DeepEqual(test.args, got) {
				t.Fatal("rewrite aliased or failed to change caller arguments")
			}
		})
	}
	for _, invalid := range [][]string{
		{"--suite", "/suite"},
		{"--credential-fd", "3", "--credential-fd=4"},
		{"--credential-fd"},
	} {
		if _, err := rewriteCredentialDescriptor(invalid, 3); err == nil {
			t.Fatalf("invalid credential arguments accepted: %q", invalid)
		}
	}
}

func TestVerifyPrivateMountPropagationRejectsEveryPropagationKind(t *testing.T) {
	directory := t.TempDir()
	private := "41 1 8:1 / / rw,nosuid - ext4 /dev/root rw\n"
	path := filepath.Join(directory, "mountinfo")
	if err := os.WriteFile(path, []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateMountPropagation(path); err != nil {
		t.Fatalf("private mountinfo rejected: %v", err)
	}
	for _, field := range []string{"shared:2", "master:3", "propagate_from:4", "unbindable"} {
		raw := strings.Replace(private, " - ", " "+field+" - ", 1)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateMountPropagation(path); err == nil {
			t.Fatalf("propagation field %q accepted", field)
		}
	}
}

func TestPrivateMountChildCannotBeClaimedWithoutReexecMarker(t *testing.T) {
	t.Setenv(mountNamespaceChildEnvironment, "forged")
	if err := enterPrivateMountNamespaceChild(); err == nil {
		t.Fatal("forged private-mount child marker was accepted")
	}
}
