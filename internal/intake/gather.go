package intake

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// input is what one call learned about an issue from outside the store.
type input struct {
	policy   *policy.Snapshot
	issue    github.Issue
	comments []github.Comment
	labels   Labels
	now      time.Time
}

// gather reads the policy in force and the issue as GitHub shows it now. A
// non-empty reason means the issue is not one for this factory to take in.
// The comments are read by the caller, once it knows it needs them.
func (s *Service) gather(ctx context.Context, ref core.IssueRef, known bool) (input, string, error) {
	in := input{now: s.cfg.Clock.Now()}
	var err error
	if in.policy, err = s.cfg.Policy.Current(ctx); err != nil {
		return in, "", err
	}
	m := in.policy.Manifest
	if m == nil {
		return in, "", core.NewError(core.CodePolicyHold, "the policy in force has no manifest")
	}
	if ref.Owner != m.Repository.Owner || ref.Repo != m.Repository.Repo {
		return in, "the issue is not in this factory's repository", nil
	}
	rules, err := RulesFrom(m)
	if err != nil {
		return in, "", err
	}
	if in.issue, err = s.cfg.Issues.GetIssue(ctx, ref); err != nil {
		return in, "", err
	}
	in.labels = ReadLabels(in.issue.Labels, rules)
	switch {
	case in.issue.State != github.IssueOpen:
		return in, "the issue is not open", nil
	case known:
	case !in.labels.Ready:
		return in, "the issue does not ask for intake", nil
	case in.labels.QuestionOnly:
		return in, "a question is not work to scope", nil
	case !in.labels.AddressedTo(m.DeploymentID):
		return in, "the issue is addressed to another deployment", nil
	}
	return in, "", nil
}
