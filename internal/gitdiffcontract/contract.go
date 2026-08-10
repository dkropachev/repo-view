// Package gitdiffcontract defines the versioned Git diff behavior shared by
// repo-view's direct backend and tokenbench's precomputed changed-state cache.
// It has no dependency on either consumer so semantic changes must be made in
// one place.
package gitdiffcontract

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
)

const (
	// Version identifies changes that can alter patch bytes, rename
	// classification, or changed-line locations.
	Version = "repo-view.git-diff/v1"

	RenameSimilarity = 50
	RenameLimit      = 20_000
)

var changedHunkPattern = regexp.MustCompile(
	`(?m)^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`,
)

// LineSpan is one inclusive, one-based range on the new side of a diff.
type LineSpan struct {
	Start int
	End   int
}

// InvocationPrefix returns the Git options that isolate all contract calls
// from user configuration while fixing rename-related defaults.
func InvocationPrefix() []string {
	return []string{
		"--literal-pathspecs",
		"-c", "color.ui=false",
		"-c", "core.quotePath=true",
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "core.untrackedCache=false",
		"-c", "diff.external=",
		"-c", "diff.renames=true",
		"-c", "diff.renameLimit=" + strconv.Itoa(RenameLimit),
		"-c", "merge.renameLimit=" + strconv.Itoa(RenameLimit),
	}
}

// Environment returns the deterministic Git environment shared by both
// consumers. Callers may append platform-required variables such as
// SystemRoot on Windows.
func Environment(nullDevice string) []string {
	if nullDevice == "" {
		nullDevice = os.DevNull
	}
	return []string{
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + nullDevice,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + nullDevice,
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + nullDevice,
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
}

// NameStatusArguments returns the canonical metadata query used to preserve
// additions, deletions, modifications, type changes, copies, and renames.
func NameStatusArguments(base, head string) []string {
	return metadataArguments("--name-status", base, head)
}

// NameOnlyArguments returns the direct backend's destination-oriented changed
// path query under the same rename classification contract.
func NameOnlyArguments(base, head string) []string {
	return metadataArguments("--name-only", base, head)
}

// NumstatArguments returns the canonical binary-classification query.
func NumstatArguments(base, head string) []string {
	return metadataArguments("--numstat", base, head)
}

func metadataArguments(format, base, head string) []string {
	return []string{
		"diff", "--no-ext-diff", "--no-textconv", format, "-z",
		"--find-renames=" + strconv.Itoa(RenameSimilarity) + "%",
		"-l" + strconv.Itoa(RenameLimit),
		base + "..." + head, "--",
	}
}

// PatchArguments fixes every option that can alter returned patch framing or
// hunk placement, including the contract's three context lines. The final
// "--" is intentional; append literal paths only.
func PatchArguments(base, head string) []string {
	return patchArguments(base, head, 3)
}

// ChangedLineArguments is the exact unified-zero variant used solely to
// derive new-side changed-line spans.
func ChangedLineArguments(base, head string) []string {
	return patchArguments(base, head, 0)
}

func patchArguments(base, head string, contextLines int) []string {
	return []string{
		"diff", "--no-color", "--no-ext-diff", "--no-textconv",
		"--find-renames=" + strconv.Itoa(RenameSimilarity) + "%",
		"-l" + strconv.Itoa(RenameLimit),
		"--diff-algorithm=myers", "--no-indent-heuristic",
		"--full-index", "--src-prefix=a/", "--dst-prefix=b/",
		"--unified=" + strconv.Itoa(contextLines), "--inter-hunk-context=0",
		base + "..." + head, "--",
	}
}

// ParseChangedSpans parses and merges new-side hunk ranges. A pure deletion
// is anchored to the nearest one-based HEAD line, matching repo-view's public
// response semantics.
func ParseChangedSpans(
	patch []byte,
	maximumSpans, maximumLine int,
) ([]LineSpan, error) {
	if maximumSpans <= 0 || maximumLine <= 0 {
		return nil, errors.New("changed-line parser limits must be positive")
	}
	matches := changedHunkPattern.FindAllSubmatch(patch, maximumSpans+1)
	if len(matches) > maximumSpans {
		return nil, errors.New("changed-line spans exceed their limit")
	}
	spans := make([]LineSpan, 0, len(matches))
	for _, match := range matches {
		start, err := strconv.Atoi(string(match[1]))
		if err != nil || start < 0 || start > maximumLine {
			return nil, errors.New("changed-line hunk start is invalid")
		}
		count := 1
		if len(match[2]) != 0 {
			count, err = strconv.Atoi(string(match[2]))
			if err != nil || count < 0 || count > maximumLine {
				return nil, errors.New("changed-line hunk count is invalid")
			}
		}
		if count == 0 {
			if start == 0 {
				start = 1
			}
			count = 1
		}
		if start == 0 || start > maximumLine-count+1 {
			return nil, errors.New("changed-line hunk range is invalid")
		}
		spans = append(spans, LineSpan{Start: start, End: start + count - 1})
	}
	return mergeSpans(spans), nil
}

func mergeSpans(spans []LineSpan) []LineSpan {
	if len(spans) == 0 {
		return []LineSpan{}
	}
	sort.Slice(spans, func(left, right int) bool {
		if spans[left].Start != spans[right].Start {
			return spans[left].Start < spans[right].Start
		}
		return spans[left].End < spans[right].End
	})
	merged := make([]LineSpan, 0, len(spans))
	for _, span := range spans {
		last := len(merged) - 1
		if last >= 0 && span.Start <= merged[last].End+1 {
			if span.End > merged[last].End {
				merged[last].End = span.End
			}
			continue
		}
		merged = append(merged, span)
	}
	return merged
}
