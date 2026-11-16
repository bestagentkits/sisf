package policy

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Accept checks that base is a commit a run of the policy in force may be
// built, judged and released on, and returns the commit it accepted: the
// approved revision itself, or a commit the supervisor followed from it.
// The record is not trusted on its own: base must contain the approved
// revision, change no path only the Owner may change since it, and hold
// the foundation files the Owner approved. While the factory is stopped,
// no base is accepted, the approved revision included, so no run builds,
// is released or merges until the Owner approves a revision. Anything else
// is POLICY_HOLD, so a commit an agent names cannot become what a candidate
// is compared against.
func (b *BuildBases) Accept(ctx context.Context, base core.CommitSHA) (core.CommitSHA, error) {
	if err := base.Validate(); err != nil {
		return "", core.NewError(core.CodePolicyHold, "the run names no valid commit to build on")
	}
	baseline, record, _, err := b.current(ctx)
	if err != nil {
		return "", err
	}
	if record.Stopped != nil {
		return "", baseHold("the factory stopped building on main at %s (%s); the Owner approves a policy revision before the factory builds, releases or merges again",
			record.Stopped.Merge, record.Stopped.Reason)
	}
	if base == baseline.Revision {
		return base, nil
	}
	if !record.followed(base) {
		return "", baseHold("commit %s is neither the approved policy revision %s nor a merge the supervisor followed from it", base, baseline.Revision)
	}
	apart, err := b.cfg.Commits.ChangedPaths(ctx, base, baseline.Revision)
	switch {
	case err != nil && unanswered(err):
		return "", err
	case err != nil:
		return "", baseHold("GitHub cannot show that %s contains the approved revision %s", base, baseline.Revision)
	case len(apart) > 0:
		return "", baseHold("%s does not contain the approved revision %s", base, baseline.Revision)
	}
	changed, err := b.cfg.Commits.ChangedPaths(ctx, baseline.Revision, base)
	switch {
	case err != nil && unanswered(err):
		return "", err
	case err != nil:
		return "", baseHold("GitHub cannot list every path %s changes since the approved revision %s", base, baseline.Revision)
	}
	if owners := b.ownerOnly(changed); len(owners) > 0 {
		return "", baseHold("%s changes %s since the approved revision, which only the Owner approves", base, strings.Join(owners, ", "))
	}
	digest, err := b.digest(ctx, base)
	if err != nil {
		return "", err
	}
	if digest != baseline.Digest {
		return "", baseHold("the foundation files at %s are not the content the Owner approved", base)
	}
	return base, nil
}

// ownerOnly returns the paths of changed only the Owner may change.
func (b *BuildBases) ownerOnly(changed []string) []string {
	var owners []string
	for _, p := range changed {
		if b.cfg.OwnerOnly(p) {
			owners = append(owners, p)
		}
	}
	return owners
}
