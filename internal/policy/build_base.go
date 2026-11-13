package policy

import (
	"context"
	"encoding/json"
	"slices"
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

// CommitParents names the parents of a commit in order, its first parent
// first: for a merge, the commit of the branch it was made on.
type CommitParents interface {
	CommitParents(ctx context.Context, commit core.CommitSHA) ([]core.CommitSHA, error)
}

// BuildBasesConfig is what the commit to build on is kept with. Every
// field but Clock is required.
type BuildBasesConfig struct {
	State     state.Store
	Source    Source
	Baselines BaselineReader
	Commits   CommitComparer
	Parents   CommitParents
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
	if cfg.State == nil || cfg.Source == nil || cfg.Baselines == nil || cfg.Commits == nil || cfg.Parents == nil || cfg.OwnerOnly == nil || cfg.Audit == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the commit to build on needs a store, a source, the baseline, a comparer, the commits' parents, the Owner-only paths and an audit log")
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
