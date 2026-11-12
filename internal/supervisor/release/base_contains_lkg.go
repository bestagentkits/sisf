package release

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// CommitComparer lists the paths head changes since it parted from base,
// as GitHub's three-dot comparison does: none exactly when base contains
// head. A listing that may be cut short fails with LIMIT_EXCEEDED.
type CommitComparer interface {
	ChangedPaths(ctx context.Context, base, head core.CommitSHA) ([]string, error)
}

// checkBaseHasLKG refuses a verified candidate whose base, the policy
// commit its QA receipt names, does not contain the commit of the LKG
// release of its unit. The candidate was built and judged on that base, so
// releasing it would serve, and merge, a program without the change the
// LKG release made, and every later release of the unit would drop it too.
// The trust baseline is moved to a commit that contains it only by the
// Owner. An LKG the install pins names no commit and is not checked, nor is
// one an earlier build recorded.
//
// A comparison that is cut short, or that GitHub refuses, for example for
// a commit it does not know, cannot show the base holds the commit, and
// refuses the candidate for the Owner as well. One GitHub could not answer
// now decides nothing: the error is returned as is and the release waits.
// An LKG that cannot be read is never passed over: one that cannot be read
// now (UNAVAILABLE) makes the release wait as well, and one that can be
// read but is not an entry the supervisor wrote holds it with RELEASE_HOLD
// for the Owner, as the preflight holds it.
func (p *Pipeline) checkBaseHasLKG(ctx context.Context, v verifiedRelease) error {
	unit := v.artifact.Unit
	lkg, found, err := p.cfg.LKG.Get(ctx, unit)
	switch {
	case core.HasCode(err, core.CodeUnavailable):
		return core.NewError(core.CodeUnavailable, "the last known good release of "+unit+
			" cannot be read now, so the candidate's base cannot be shown to contain its commit").WithCause(err)
	case err != nil:
		return holdf("the last known good release of %s cannot be read", unit).WithCause(err)
	}
	base := v.receipt.PolicySHA
	if !found || lkg.Source != SourcePromoted || lkg.Commit == "" || lkg.Commit == base {
		return nil
	}
	refused := func(why string) error {
		err := core.NewError(core.CodeApprovalRequired, "the candidate is built on "+string(base)+", which "+why+" "+string(lkg.Commit)+
			", the commit of "+string(lkg.RunID)+", the last known good release of unit "+unit+
			"; releasing it would drop that release's change, so the Owner moves the trust baseline to a commit that contains it before the unit is released again")
		err.Details = map[string]string{"base": string(base), "lkg_commit": string(lkg.Commit), "lkg_run": string(lkg.RunID), "unit": unit}
		return err
	}
	if p.cfg.Commits == nil {
		return refused("cannot be shown to contain")
	}
	apart, err := p.cfg.Commits.ChangedPaths(ctx, base, lkg.Commit)
	switch {
	case err == nil && len(apart) == 0:
		return nil
	case err == nil:
		return refused("does not contain")
	case comparisonUnanswered(err):
		return err
	}
	return refused("GitHub's comparison cannot show contains")
}

// comparisonUnanswered reports whether GitHub could not answer a
// comparison now, rather than answering it in a way that judges.
func comparisonUnanswered(err error) bool {
	switch core.CodeOf(err) {
	case core.CodeUnavailable, core.CodeRateLimited, core.CodeTimeout, core.CodeInternal, core.CodeUnauthenticated:
		return true
	}
	return false
}
