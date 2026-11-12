package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/decision"
)

// SoW is one version of the Scope of Work and Definition of Done of an
// issue. A version is never changed: an edit that matters makes a new
// version with a new digest, and an approval of the old digest does not
// cover it.
type SoW struct {
	Issue core.IssueRef `json:"issue"`
	// Version counts from 1.
	Version int `json:"version"`
	// Kind is the kind of work after classification, which may be more
	// cautious than the label on the issue.
	Kind     decision.Kind `json:"kind"`
	Title    string        `json:"title"`
	Scope    string        `json:"scope"`
	DoD      []string      `json:"definition_of_done"`
	Features []string      `json:"affected_features,omitempty"`
	// Digest identifies the content above. It is what an approval names.
	Digest core.Digest `json:"digest"`
	// PolicyRevision is the approved policy the scope was written under.
	PolicyRevision core.CommitSHA `json:"policy_revision"`
	CreatedAt      time.Time      `json:"created_at"`
}

// ScopeDigest returns the digest of a scope: the issue, the kind of work,
// the title, the description, the criteria and the features, each quoted on
// its own line.
//
// The draft is already normalized, so an edit that only moves whitespace or
// reformats a list leaves the digest unchanged. Every other edit is
// significant and changes it.
func ScopeDigest(issue core.IssueRef, kind decision.Kind, draft Draft) core.Digest {
	var b strings.Builder
	b.WriteString("sisf.sow.v1\n")
	fmt.Fprintf(&b, "issue=%q\nkind=%q\ntitle=%q\nscope=%q\n", issue.String(), string(kind), draft.Title, draft.Scope)
	for _, item := range draft.DoD {
		fmt.Fprintf(&b, "dod=%q\n", item)
	}
	for _, feature := range draft.Features {
		fmt.Fprintf(&b, "feature=%q\n", feature)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// newSoW writes version number version of the scope from a complete draft.
func newSoW(issue core.IssueRef, version int, kind decision.Kind, draft Draft, revision core.CommitSHA, at time.Time) SoW {
	return SoW{Issue: issue, Version: version, Kind: kind, Title: draft.Title, Scope: draft.Scope, DoD: draft.DoD,
		Features: draft.Features, Digest: ScopeDigest(issue, kind, draft), PolicyRevision: revision, CreatedAt: at}
}

// maxQuoted bounds how much of a scope is repeated in a comment or an
// approval request. The digest always covers the whole text.
const maxQuoted = 8000

// Text renders the scope as plain text, for the approval inbox and for the
// comment that attaches it to the issue.
func (s SoW) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nKind: %s\n\nScope:\n%s\n\nDefinition of Done:\n", s.Title, s.Kind, truncate(s.Scope))
	for i, item := range s.DoD {
		fmt.Fprintf(&b, "%d. %s\n", i+1, item)
	}
	if len(s.Features) > 0 {
		fmt.Fprintf(&b, "\nAffected features: %s\n", strings.Join(s.Features, ", "))
	}
	return b.String()
}

// truncate shortens text that is too long to quote and says so.
func truncate(text string) string {
	if len(text) <= maxQuoted {
		return text
	}
	cut := maxQuoted
	// Do not split a multi-byte character.
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + "\n[shortened here; the digest covers the full text]"
}

// fenced wraps text taken from an issue in a code fence longer than any run
// of backticks inside it. Inside the fence nothing is rendered: a mention
// notifies nobody, and markup or a hidden marker in the text stays text.
func fenced(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "text\n" + strings.TrimRight(text, "\n") + "\n" + fence
}
