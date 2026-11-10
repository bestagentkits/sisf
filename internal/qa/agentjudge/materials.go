package agentjudge

import (
	"context"
	"path"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Bounds of what a judgment is given. Larger inputs are cut with a note in
// the prompt, never refused: the agent still sees the start of each.
const (
	// MaxPromptDiffBytes is how much of the diff the prompt quotes; the
	// whole diff is in the workspace.
	MaxPromptDiffBytes = 192 << 10
	// MaxTextBytes bounds each other text quoted in the prompt.
	MaxTextBytes = 32 << 10
	// MaxFiles and MaxFileBytes bound the candidate files in the workspace.
	MaxFiles     = 64
	MaxFileBytes = 256 << 10
	// MaxDiffBytes bounds the diff a judgment accepts at all.
	MaxDiffBytes = 1 << 20
)

// Materials is everything the QA agent is shown of one candidate. All of it
// is untrusted data from the candidate, its issue or its tools, except the
// review criteria, which are read at the Owner-approved commit.
type Materials struct {
	// Scope is the approved scope of work, taken from the issue.
	Scope string
	// AcceptanceCriteria is the issue's definition of done.
	AcceptanceCriteria string
	// ReviewCriteria is the review criteria file at the pinned commit.
	ReviewCriteria string
	// Diff is the candidate's change against its baseline.
	Diff  string
	Build BuildResult
	// Files are the changed files as they are at the candidate's commit.
	Files []File
}

// BuildResult is what the supervisor's own build recorded of the candidate.
type BuildResult struct {
	Builder     string
	TestsPassed bool
	Evidence    string
}

// File is one file of the candidate at its commit.
type File struct {
	Path    string
	Content []byte
}

// MaterialsSource gathers the materials of a candidate from the
// supervisor's records and the repository, never from the candidate's
// claims about itself.
type MaterialsSource interface {
	Materials(ctx context.Context, c qa.Candidate) (Materials, error)
}

// MaterialsFunc adapts a function to MaterialsSource.
type MaterialsFunc func(ctx context.Context, c qa.Candidate) (Materials, error)

// Materials calls f.
func (f MaterialsFunc) Materials(ctx context.Context, c qa.Candidate) (Materials, error) {
	return f(ctx, c)
}

// check refuses materials a judgment cannot be made from safely: no diff,
// a diff over the limit, or a file path that could leave the workspace.
// Files beyond the limits are dropped; the diff still shows their change.
func (m Materials) check() (Materials, error) {
	switch {
	case strings.TrimSpace(m.Diff) == "":
		return Materials{}, core.NewError(core.CodeInvalidArgument, "a candidate without a diff cannot be judged")
	case len(m.Diff) > MaxDiffBytes:
		return Materials{}, core.NewError(core.CodeInvalidArgument, "the candidate's diff is too large to be judged")
	}
	kept := make([]File, 0, min(len(m.Files), MaxFiles))
	seen := map[string]bool{}
	for _, f := range m.Files {
		clean := path.Clean(f.Path)
		if f.Path == "" || clean != f.Path || path.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." ||
			strings.ContainsAny(clean, "\\\x00") {
			return Materials{}, core.NewError(core.CodeInvalidArgument, "a candidate file has a path outside the repository")
		}
		if seen[clean] || len(kept) == MaxFiles || len(f.Content) > MaxFileBytes {
			continue
		}
		seen[clean] = true
		kept = append(kept, f)
	}
	m.Files = kept
	return m, nil
}

// clip shortens s to n bytes on a rune boundary and says what was left out.
func clip(s string, n int, rest string) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !startsRune(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[cut: " + itoa(len(s)-cut) + " more bytes" + rest + "]"
}

// startsRune reports whether b begins a UTF-8 sequence.
func startsRune(b byte) bool { return b&0xC0 != 0x80 }
