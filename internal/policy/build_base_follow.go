package policy

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// FollowProof names a merge the supervisor made of a release it verified.
type FollowProof struct {
	// Merge is the merge commit on the default branch; Candidate the
	// candidate commit QA judged, which Merge must contain.
	Merge     core.CommitSHA
	Candidate core.CommitSHA
	Run       core.RunID
	Unit      string
}

// Follow moves the commit to build on to proof.Merge.
//
// Only the supervisor does this, and only when every check holds again on
// what GitHub shows now, whatever the proof claims:
//
//   - the merge contains the commit to build on now and the candidate;
//   - between the two it changes no path only the Owner may change, the
//     foundation files, ADRs, CI, ownership and migrations among them;
//   - the foundation files at the merge are the content the Owner approved.
//
// A merge the commit to build on already contains changes nothing. When a
// check fails, the factory stops: the record says which merge was not
// followed and why, no run is claimed until the Owner approves a revision,
// and the answer is APPROVAL_REQUIRED. A GitHub that cannot answer now
// decides nothing and the error is returned as is. Approving a revision
// remains Baselines.Approve's alone; Follow never changes the policy in
// force or its approvals. Every attempt is audited.
func (b *BuildBases) Follow(ctx context.Context, actor core.Actor, proof FollowProof) (BuildBase, error) {
	if actor.Kind != core.ActorService || actor.ID != SupervisorActorID {
		err := core.NewError(core.CodePermissionDenied, "only the release supervisor moves the commit to build on")
		_ = b.audit(ctx, actor, proof, "", core.OutcomeDenied, err)
		return BuildBase{}, err
	}
	if err := proof.Candidate.Validate(); err != nil {
		return BuildBase{}, err
	}
	baseline, record, version, err := b.current(ctx)
	if err != nil {
		return BuildBase{}, err
	}
	if record.Stopped != nil {
		err := core.NewError(core.CodeApprovalRequired, "the factory stopped following main at "+string(record.Stopped.Merge)+
			" ("+record.Stopped.Reason+"); the Owner approves a policy revision that contains it before the factory releases again")
		_ = b.audit(ctx, actor, proof, baseline.Revision, core.OutcomeDenied, err)
		return record, err
	}
	why := "the merge commit of the release is not known"
	if proof.Merge.Validate() == nil {
		if proof.Merge == record.Commit || record.followed(proof.Merge) {
			return record, nil
		}
		changed, err := b.cfg.Commits.ChangedPaths(ctx, record.Commit, proof.Merge)
		switch {
		case err != nil && unanswered(err):
			return BuildBase{}, err
		case err == nil && len(changed) == 0:
			// A later merge was followed first: this one is already in it.
			return record, nil
		}
		if why, err = b.judge(ctx, baseline, record, proof, changed, err); err != nil {
			return BuildBase{}, err
		}
	}
	return b.write(ctx, actor, proof, baseline, record, version, why)
}

// MainCheck names the head of the default branch the supervisor is about
// to merge the release of Run, of Unit, into.
type MainCheck struct {
	Head core.CommitSHA
	Run  core.RunID
	Unit string
}

// CheckMain refuses to let the supervisor merge into a default branch whose
// content the Owner has not approved. It answers nil when head is a commit
// the factory builds on, or changes no path only the Owner may change since
// the commit to build on. Otherwise, or when main cannot be shown to leave
// those paths alone, the factory stops as a refused follow stops it, and
// the answer is APPROVAL_REQUIRED; while the factory is stopped, it is
// APPROVAL_REQUIRED too, so no run merges until the Owner approves a
// revision. A commit pushed to main that changes nothing the Owner approves
// does not stop the merge here; following the merge then stops the factory
// (Follow). Only the supervisor checks, and a refusal is audited.
func (b *BuildBases) CheckMain(ctx context.Context, actor core.Actor, check MainCheck) error {
	proof := FollowProof{Merge: check.Head, Run: check.Run, Unit: check.Unit}
	if actor.Kind != core.ActorService || actor.ID != SupervisorActorID {
		err := core.NewError(core.CodePermissionDenied, "only the release supervisor merges into main")
		_ = b.audit(ctx, actor, proof, "", core.OutcomeDenied, err)
		return err
	}
	if err := check.Head.Validate(); err != nil {
		return core.NewError(core.CodeUnavailable, "the head of main is not known").WithCause(err)
	}
	baseline, record, version, err := b.current(ctx)
	if err != nil {
		return err
	}
	if record.Stopped != nil {
		return core.NewError(core.CodeApprovalRequired, "the factory stopped building on main at "+string(record.Stopped.Merge)+
			" ("+record.Stopped.Reason+"); the Owner approves a policy revision before the factory merges again")
	}
	if check.Head == baseline.Revision || check.Head == record.Commit || record.followed(check.Head) {
		return nil
	}
	changed, err := b.cfg.Commits.ChangedPaths(ctx, record.Commit, check.Head)
	why := ""
	switch {
	case err != nil && unanswered(err):
		return err
	case err != nil:
		why = "GitHub cannot list every path main changes since the commit the factory builds on"
	default:
		if owners := b.ownerOnly(changed); len(owners) > 0 {
			why = "main changes " + strings.Join(owners, ", ") + ", which only the Owner approves"
		}
	}
	if why == "" {
		return nil
	}
	_, err = b.write(ctx, actor, proof, baseline, record, version, why)
	return err
}

// write records the outcome of an attempt to move the commit to build on:
// a stop when why is set, the move to proof.Merge otherwise. The refusal
// a stop answers with is returned with the record.
func (b *BuildBases) write(ctx context.Context, actor core.Actor, proof FollowProof, baseline Baseline, record BuildBase, version int64, why string) (BuildBase, error) {
	next := record
	if why != "" {
		next.Stopped = &BaseStop{Merge: proof.Merge, Run: proof.Run, Unit: proof.Unit, Reason: why, At: b.cfg.Clock.Now().UTC()}
	} else {
		next.Commit = proof.Merge
		next.Chain = append(slices.Clone(record.Chain), proof.Merge)
		next.Run, next.Unit, next.FollowedAt = proof.Run, proof.Unit, b.cfg.Clock.Now().UTC()
	}
	outcome := core.OutcomeExecuted
	var refusal error
	if why != "" {
		outcome = core.OutcomeDenied
		refusal = core.NewError(core.CodeApprovalRequired, "the factory stopped building on main at "+string(proof.Merge)+": "+why+
			"; the Owner approves a policy revision that contains it before the factory releases again")
	}
	if err := b.audit(ctx, actor, proof, baseline.Revision, core.OutcomeAllowed, nil); err != nil {
		return BuildBase{}, core.NewError(core.CodeOf(err), "following main could not be audited, so the commit to build on did not move").WithCause(err)
	}
	if err := b.put(ctx, next, version); err != nil {
		_ = b.audit(ctx, actor, proof, baseline.Revision, core.OutcomeFailed, err)
		return BuildBase{}, err
	}
	_ = b.audit(ctx, actor, proof, baseline.Revision, outcome, refusal)
	return next, refusal
}

// judge says why proof.Merge may not be followed, "" when it may. An error
// means GitHub could not answer now.
func (b *BuildBases) judge(ctx context.Context, baseline Baseline, record BuildBase, proof FollowProof, changed []string, listErr error) (string, error) {
	if len(record.Chain) >= maxChain {
		return "the approved revision was followed through as many merges as one revision may be", nil
	}
	for _, check := range []struct {
		base, head core.CommitSHA
		why        string
	}{
		{proof.Merge, record.Commit, "the merge does not contain the commit the factory builds on"},
		{proof.Merge, proof.Candidate, "the merge does not contain the candidate QA judged"},
	} {
		apart, err := b.cfg.Commits.ChangedPaths(ctx, check.base, check.head)
		switch {
		case err != nil && unanswered(err):
			return "", err
		case err != nil:
			return check.why + " as far as GitHub can show", nil
		case len(apart) > 0:
			return check.why, nil
		}
	}
	if listErr != nil {
		return "GitHub cannot list every path the merge changes", nil
	}
	if owners := b.ownerOnly(changed); len(owners) > 0 {
		return "main changes " + strings.Join(owners, ", ") + ", which only the Owner approves", nil
	}
	if why, err := b.onlyTheCandidate(ctx, baseline, record, proof); why != "" || err != nil {
		return why, err
	}
	digest, err := b.digest(ctx, proof.Merge)
	switch {
	case err != nil && unanswered(err):
		return "", err
	case err != nil:
		return "the foundation files at the merge do not form a policy", nil
	case digest != baseline.Digest:
		return "the foundation files at the merge are not the content the Owner approved", nil
	}
	return "", nil
}

// onlyTheCandidate says why proof.Merge brings in more than the candidate
// QA judged, "" when it brings in that alone. The merge must have been made
// on a commit the factory builds on, the approved revision or a merge it
// followed, so a commit pushed to main outside the factory is never taken
// in; and every path the merge changes over that commit must hold exactly
// what the candidate holds, so nothing but the judged change lands. An
// error means GitHub could not answer now.
func (b *BuildBases) onlyTheCandidate(ctx context.Context, baseline Baseline, record BuildBase, proof FollowProof) (string, error) {
	parents, err := b.cfg.Parents.CommitParents(ctx, proof.Merge)
	switch {
	case err != nil && unanswered(err):
		return "", err
	case err != nil || len(parents) == 0:
		return "GitHub cannot name the commit the merge was made on", nil
	}
	on := parents[0]
	if on != baseline.Revision && !record.followed(on) {
		return "the merge was made on " + string(on) + ", a commit on main the factory neither made nor followed, so it may bring in a change QA never judged", nil
	}
	added, err := b.cfg.Commits.ChangedPaths(ctx, on, proof.Merge)
	switch {
	case err != nil && unanswered(err):
		return "", err
	case err != nil:
		return "GitHub cannot list every path the merge adds", nil
	}
	for _, path := range added {
		same, err := b.sameFile(ctx, proof.Merge, proof.Candidate, path)
		switch {
		case err != nil:
			return "", err
		case !same:
			return "the merge brings in " + path + " other than the candidate QA judged holds it", nil
		}
	}
	return "", nil
}

// sameFile reports whether path holds the same content at a and b, or is
// absent from both. An error means GitHub could not answer now; a file
// that cannot be read for another reason is not shown to be the same.
func (b *BuildBases) sameFile(ctx context.Context, a, c core.CommitSHA, path string) (bool, error) {
	read := func(commit core.CommitSHA) ([]byte, bool, error) {
		content, err := b.cfg.Source.ReadFile(ctx, commit, path)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil, false, nil
		case err != nil && unanswered(err):
			return nil, false, err
		case err != nil:
			return nil, false, errUnreadable
		}
		return content, true, nil
	}
	x, inA, err := read(a)
	if err == nil {
		var y []byte
		var inC bool
		if y, inC, err = read(c); err == nil {
			return inA == inC && string(x) == string(y), nil
		}
	}
	if errors.Is(err, errUnreadable) {
		return false, nil
	}
	return false, err
}

// errUnreadable marks a file GitHub refused to show, which is never shown
// to be the candidate's.
var errUnreadable = errors.New("the file cannot be read")

// digest reads the content digest of the foundation files at commit.
func (b *BuildBases) digest(ctx context.Context, commit core.CommitSHA) (core.Digest, error) {
	snapshot, err := read(ctx, b.cfg.Source, commit)
	if err != nil {
		var typed *core.Error
		if errors.As(err, &typed) && typed.Details["reason"] == reasonUnavailable {
			return "", core.NewError(core.CodeUnavailable, "the foundation files at "+string(commit)+" cannot be read now").WithCause(err)
		}
		return "", err
	}
	if snapshot.Manifest.FactoryID != b.cfg.FactoryID {
		return "", hold(reasonInvalid, "%s at %s declares another factory", PathFactory, commit)
	}
	return snapshot.Digest, nil
}
