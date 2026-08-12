package main

import (
	"strings"
	"testing"
)

func TestValidReleaseRevision(t *testing.T) {
	t.Parallel()
	if !validReleaseRevision(strings.Repeat("a", 40)) {
		t.Fatal("lowercase 40-hex release revision was rejected")
	}
	for _, value := range []string{
		"", "development", strings.Repeat("a", 39), strings.Repeat("a", 41),
		strings.Repeat("A", 40), strings.Repeat("g", 40),
	} {
		if validReleaseRevision(value) {
			t.Errorf("invalid operational release revision accepted: %q", value)
		}
	}
}
