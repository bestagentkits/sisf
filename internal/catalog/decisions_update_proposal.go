package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// PathDecisions is the decision rules file the Owner approves.
const PathDecisions = policy.PathDecisions

// maxRationaleLength bounds the rationale written into a pull request.
const maxRationaleLength = 2000

// DecisionsUpdate is a proposed new content of DECISIONS.md.
type DecisionsUpdate struct {
	Content   string `json:"content"`
	Summary   string `json:"summary"`
	Rationale string `json:"rationale,omitempty"`
}

// DecisionsProposal is the pull request a DecisionsUpdate became. It is
// never applied: the Owner approves it by merging, or it has no effect.
type DecisionsProposal struct {
	Branch string         `json:"branch"`
	Base   core.CommitSHA `json:"base"`
	// PullRequest is "owner/repo#pull/N"; empty for a dry run.
	PullRequest           string `json:"pull_request,omitempty"`
	RequiresOwnerApproval bool   `json:"requires_owner_approval"`
	Applied               bool   `json:"applied"`
}

// ProposeDecisionsUpdate opens a pull request that replaces DECISIONS.md
// with u.Content, made as the Publisher on a new branch off the base
// branch's head. It never writes the base branch and never merges: the
// change takes effect only when the Owner merges it, whoever asked for it.
// Content that would put the decision layer on hold, or that changes the
// typed questions without a new schema version, is INVALID_ARGUMENT; the
// content the base branch already has is ALREADY_EXISTS. With plan set,
// nothing is written and the pull request that would be opened is returned.
func (p *Publisher) ProposeDecisionsUpdate(ctx context.Context, requester core.Actor, u DecisionsUpdate, plan bool) (DecisionsProposal, error) {
	content := []byte(u.Content)
	switch {
	case len(content) == 0 || len(content) > policy.MaxFileSize || !utf8.Valid(content):
		return DecisionsProposal{}, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("the proposed %s must be UTF-8 text of 1 to %d bytes", PathDecisions, policy.MaxFileSize))
	case plain(u.Summary) == "" || requester.ID == "":
		return DecisionsProposal{}, core.NewError(core.CodeInvalidArgument, "a decisions update needs a summary and a requester")
	}
	proposed, err := decision.ParseTypedQuestions(content)
	if err != nil {
		return DecisionsProposal{}, core.NewError(core.CodeInvalidArgument, "the proposed "+PathDecisions+" would put the decision layer on hold").WithCause(err)
	}
	base, err := p.cfg.Branches.Head(ctx, p.cfg.Repo, p.cfg.BaseBranch)
	if err != nil {
		return DecisionsProposal{}, err
	}
	current, err := p.read(ctx, base, PathDecisions)
	if err != nil {
		return DecisionsProposal{}, err
	}
	if bytes.Equal(current, content) {
		return DecisionsProposal{}, core.NewError(core.CodeAlreadyExists, "the base branch already has this "+PathDecisions)
	}
	if was, err := decision.ParseTypedQuestions(current); err == nil && was.SchemaVersion == proposed.SchemaVersion &&
		!reflect.DeepEqual(was.Questions, proposed.Questions) {
		return DecisionsProposal{}, core.NewError(core.CodeInvalidArgument, "a change to the typed questions or their allowed values needs a new schema version")
	}
	sum := sha256.Sum256(content)
	out := DecisionsProposal{Branch: "sisf/decisions/" + hex.EncodeToString(sum[:6]), Base: base, RequiresOwnerApproval: true}
	if plan {
		return out, nil
	}
	repo := p.cfg.Repo
	if _, err := p.cfg.Branches.Commit(ctx, repo, Commit{Branch: out.Branch, Base: base, Author: p.cfg.Actor.ID,
		Message: "feat(decisions): propose an update to " + PathDecisions, Files: map[string][]byte{PathDecisions: content}}); err != nil {
		return DecisionsProposal{}, err
	}
	pr, err := p.cfg.Pulls.CreatePullRequest(ctx, repo, github.NewPullRequest{
		Title:   "Propose an update to " + PathDecisions + ": " + plain(u.Summary),
		Body:    decisionsBody(requester, u, proposed.SchemaVersion, p.cfg.BaseBranch),
		HeadRef: out.Branch, BaseRef: p.cfg.BaseBranch,
	})
	if err != nil {
		return DecisionsProposal{}, err
	}
	out.PullRequest = fmt.Sprintf("%s#pull/%d", repo, pr.Number)
	return out, nil
}

// decisionsBody tells the Owner who asked and why. The requester's text is
// data: it is reduced to one bounded line each.
func decisionsBody(requester core.Actor, u DecisionsUpdate, version int, baseBranch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Requested by %s (%s).\n\nSummary: %s\n\n", plain(requester.ID), requester.Kind, plain(u.Summary))
	if rationale := oneLine(u.Rationale, maxRationaleLength); rationale != "" {
		fmt.Fprintf(&b, "Rationale, as written by the requester: %s\n\n", rationale)
	}
	fmt.Fprintf(&b, "Typed-question schema version: %d.\n\n", version)
	fmt.Fprintf(&b, "Nothing was applied to %s. This change takes effect only when the Owner approves and merges this pull request; "+
		"the requester cannot approve it.\n", baseBranch)
	return b.String()
}

// oneLine is plain with a longer bound: no control characters, no HTML
// comments, at most limit bytes, cut at a character boundary.
func oneLine(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(strings.NewReplacer("<!--", "", "-->", "").Replace(text)), " ")
	for len(text) > limit {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return strings.TrimSpace(text)
}
