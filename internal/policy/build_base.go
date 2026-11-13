package policy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the commit to build on is kept. The namespace is the control
// zone's and the agent zone may read it but never write it, so the
// dispatcher can pin a run's workspace to the commit and see a stop,
// while nothing an agent runs can move the commit or lift the stop.
const (
	BaseNamespace = "control.policy"
	baseKey       = "build-base"
	// ActionFollowMain is the audited action of the supervisor moving the
	// commit to build on to the merge of a release it verified.
	ActionFollowMain = "policy.follow_main"
	// SupervisorActorID is the one service identity that may move the
	// commit to build on.
	SupervisorActorID = "service:supervisor"
	// maxChain bounds how many commits one approved revision is followed
	// through. The Owner approving a revision starts a new chain.
	maxChain = 1000
)

// SupervisorActor is the identity the trusted release supervisor follows
// main as.
func SupervisorActor() core.Actor {
	return core.Actor{ID: SupervisorActorID, Kind: core.ActorService}
}

// BuildBase is the commit runs are coded, built and judged on, which is
// separate from the policy revision the Owner approved. The Owner approves
// content: the foundation files at Revision, whose digest is Digest. The
// supervisor follows main from Revision through the merges of the releases
// it verified itself, and only while the foundation files and every path
// only the Owner may merge are unchanged, so the commit to build on moves
// without ever changing what the Owner approved.
type BuildBase struct {
	// Revision and Digest are the Owner-approved policy revision and its
	// content. A record of another revision than the one in force is stale
	// and plays no part.
	Revision core.CommitSHA `json:"revision"`
	Digest   core.Digest    `json:"digest"`
	// Commit is the commit to build on: the last of Chain, or Revision.
	Commit core.CommitSHA `json:"commit"`
	// Chain lists the merge commits followed since Revision, oldest first.
	// A run pinned to any of them is judged on the commit it was coded on.
	Chain []core.CommitSHA `json:"chain,omitempty"`
	// Run and Unit name the release whose merge was followed last.
	Run        core.RunID `json:"run,omitempty"`
	Unit       string     `json:"unit,omitempty"`
	FollowedAt time.Time  `json:"followed_at,omitzero"`
	// Stopped is set when a merge could not be followed. No run is claimed
	// until the Owner puts another revision in force.
	Stopped *BaseStop `json:"stopped,omitempty"`
}

// BaseStop says which merge the supervisor did not follow, and why.
type BaseStop struct {
	Merge  core.CommitSHA `json:"merge"`
	Run    core.RunID     `json:"run"`
	Unit   string         `json:"unit"`
	Reason string         `json:"reason"`
	At     time.Time      `json:"at"`
}

// BaseFor returns the commit a run pinned to revision is coded on: the
// commit followed last while revision is the one this record serves, and
// revision itself otherwise.
func (b BuildBase) BaseFor(revision core.CommitSHA) core.CommitSHA {
	if b.Revision == revision && b.Commit != "" {
		return b.Commit
	}
	return revision
}

// StopFor returns the stop that holds runs pinned to revision, if any.
func (b BuildBase) StopFor(revision core.CommitSHA) *BaseStop {
	if b.Revision != revision {
		return nil
	}
	return b.Stopped
}

// followed reports whether commit is one the supervisor followed under
// this record.
func (b BuildBase) followed(commit core.CommitSHA) bool { return slices.Contains(b.Chain, commit) }

// ReadBuildBase reads the record of the commit to build on through kv. It
// reports false when there is none.
func ReadBuildBase(ctx context.Context, kv state.KV) (BuildBase, bool, error) {
	base, _, found, err := readBuildBase(ctx, kv)
	return base, found, err
}

func readBuildBase(ctx context.Context, kv state.KV) (BuildBase, int64, bool, error) {
	entry, err := kv.GetKV(ctx, BaseNamespace, baseKey)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return BuildBase{}, 0, false, nil
	case err != nil:
		return BuildBase{}, 0, false, err
	}
	var base BuildBase
	if err := json.Unmarshal(entry.Value, &base); err != nil || base.Revision.Validate() != nil || base.Commit.Validate() != nil {
		return BuildBase{}, 0, false, core.NewError(core.CodeTampered, "the stored commit to build on cannot be read")
	}
	return base, entry.Version, true, nil
}

// ClearStaleBuildBase deletes the record of the commit to build on when it
// serves another revision than inForce, so a stop the Owner answered by
// approving a new revision no longer holds runs pinned to the old one: they
// are then superseded at build, as after any policy change.
func ClearStaleBuildBase(ctx context.Context, store state.Store, inForce core.CommitSHA) error {
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, BaseNamespace, baseKey)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		var base BuildBase
		if json.Unmarshal(entry.Value, &base) == nil && base.Revision == inForce {
			return nil
		}
		return tx.DeleteKV(ctx, BaseNamespace, baseKey, entry.Version)
	})
}

// CommitComparer lists the paths head changes since it parted from base,
// as GitHub's three-dot comparison does: none exactly when base contains
// head. A listing that may be cut short fails with LIMIT_EXCEEDED.
type CommitComparer interface {
	ChangedPaths(ctx context.Context, base, head core.CommitSHA) ([]string, error)
}

// BuildBasesConfig is what the commit to build on is kept with. Every
// field but Clock is required.
type BuildBasesConfig struct {
	State     state.Store
	Source    Source
	Baselines BaselineReader
	Commits   CommitComparer
	// OwnerOnly reports whether a path is one only the Owner may change or
	// merge: the foundation files, ADRs, CI, ownership and migrations. A
	// merge that changes one is never followed.
	OwnerOnly func(path string) bool
	Audit     core.AuditLog
	FactoryID core.FactoryID
	Clock     core.Clock
}

// BuildBases keeps the commit to build on. Only the supervisor moves it.
type BuildBases struct{ cfg BuildBasesConfig }

// NewBuildBases builds the record keeper, refusing a missing part.
func NewBuildBases(cfg BuildBasesConfig) (*BuildBases, error) {
	if cfg.State == nil || cfg.Source == nil || cfg.Baselines == nil || cfg.Commits == nil || cfg.OwnerOnly == nil || cfg.Audit == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the commit to build on needs a store, a source, the baseline, a comparer, the Owner-only paths and an audit log")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	return &BuildBases{cfg: cfg}, nil
}

// current reads the baseline in force and the record that serves it; a
// stale record is replaced by a fresh one at the approved revision.
func (b *BuildBases) current(ctx context.Context) (Baseline, BuildBase, int64, error) {
	baseline, err := b.cfg.Baselines.Current(ctx)
	if err != nil {
		return Baseline{}, BuildBase{}, 0, err
	}
	var base BuildBase
	var version int64
	var found bool
	err = b.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		base, version, found, err = readBuildBase(ctx, tx)
		return err
	})
	if err != nil {
		return Baseline{}, BuildBase{}, 0, err
	}
	if !found || base.Revision != baseline.Revision || base.Digest != baseline.Digest {
		// Stale or absent: a fresh chain starts at the approved revision,
		// written over the stale entry at the version it was read.
		base = BuildBase{Revision: baseline.Revision, Digest: baseline.Digest, Commit: baseline.Revision}
	}
	return baseline, base, version, nil
}

// Accept checks that base is a commit a run of the policy in force may be
// built and judged on: the approved revision itself, or a commit the
// supervisor followed from it. The record is not trusted on its own: the
// foundation files at base must still be the content the Owner approved
// and base must contain the approved revision. Anything else is
// POLICY_HOLD, so a commit an agent names cannot become what a candidate
// is compared against.
func (b *BuildBases) Accept(ctx context.Context, base core.CommitSHA) error {
	if err := base.Validate(); err != nil {
		return core.NewError(core.CodePolicyHold, "the run names no valid commit to build on")
	}
	baseline, record, _, err := b.current(ctx)
	if err != nil {
		return err
	}
	if base == baseline.Revision {
		return nil
	}
	if !record.followed(base) {
		return baseHold("commit %s is neither the approved policy revision %s nor a merge the supervisor followed from it", base, baseline.Revision)
	}
	digest, err := b.digest(ctx, base)
	if err != nil {
		return err
	}
	if digest != baseline.Digest {
		return baseHold("the foundation files at %s are not the content the Owner approved", base)
	}
	apart, err := b.cfg.Commits.ChangedPaths(ctx, base, baseline.Revision)
	switch {
	case err != nil && unanswered(err):
		return err
	case err != nil:
		return baseHold("GitHub cannot show that %s contains the approved revision %s", base, baseline.Revision)
	case len(apart) > 0:
		return baseHold("%s does not contain the approved revision %s", base, baseline.Revision)
	}
	return nil
}

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
	if err := proof.Merge.Validate(); err != nil {
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
	why, err := b.judge(ctx, baseline, record, proof, changed, err)
	if err != nil {
		return BuildBase{}, err
	}
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
		refusal = core.NewError(core.CodeApprovalRequired, "the factory did not follow main to "+string(proof.Merge)+": "+why+
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
	var owners []string
	for _, p := range changed {
		if b.cfg.OwnerOnly(p) {
			owners = append(owners, p)
		}
	}
	if len(owners) > 0 {
		return "main changes " + strings.Join(owners, ", ") + ", which only the Owner approves", nil
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

// put writes the record at the version it was read, so two followers never
// overwrite each other.
func (b *BuildBases) put(ctx context.Context, base BuildBase, version int64) error {
	raw, err := json.Marshal(base)
	if err != nil {
		return core.NewError(core.CodeInternal, "the commit to build on cannot be encoded").WithCause(err)
	}
	return b.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: BaseNamespace, Key: baseKey, Value: raw, Version: version})
		return err
	})
}

// audit appends one record of an attempt to follow main.
func (b *BuildBases) audit(ctx context.Context, actor core.Actor, proof FollowProof, revision core.CommitSHA, outcome core.AuditOutcome, cause error) error {
	details := map[string]string{"merge": string(proof.Merge), "candidate": string(proof.Candidate), "run": string(proof.Run), "unit": proof.Unit}
	if cause != nil {
		details["code"] = string(core.CodeOf(cause))
	}
	_, err := b.cfg.Audit.Append(ctx, core.AuditEvent{
		At: b.cfg.Clock.Now(), FactoryID: b.cfg.FactoryID, ActorID: actor.ID, ActorKind: actor.Kind, Surface: actor.Surface,
		Action: ActionFollowMain, Resource: "build-base:" + string(proof.Merge), PolicySHA: revision, Outcome: outcome, Details: details,
	})
	return err
}

// baseHold refuses a commit to build on.
func baseHold(format string, args ...any) *core.Error {
	return hold(reasonUnapproved, format, args...)
}

// unanswered reports whether GitHub could not answer now, rather than
// answering in a way that judges.
func unanswered(err error) bool {
	switch core.CodeOf(err) {
	case core.CodeUnavailable, core.CodeRateLimited, core.CodeTimeout, core.CodeInternal, core.CodeUnauthenticated:
		return true
	}
	return false
}
