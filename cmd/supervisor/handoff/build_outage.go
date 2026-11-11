package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// TopicBuildHeld is the outbox topic that tells the Owner, on the issue,
// that a candidate build is held after repeated outages.
const TopicBuildHeld = "build.held"

// BuildOutage is the supervisor's record of the outages one evaluator
// attempt's build met, kept in the build namespace until the patch is
// built or refused. Once Held, the supervisor stops trying: only the
// Owner's retry (ResumeHeldBuild) lets it try again.
type BuildOutage struct {
	RunID   core.RunID    `json:"run_id"`
	IssueID core.IssueRef `json:"issue_id"`
	Attempt int           `json:"attempt"`
	// Failures counts the outages in a row since the build last started
	// over.
	Failures int `json:"failures"`
	// LastCode is the code of the last failure; the message is not kept.
	LastCode  core.ErrorCode `json:"last_code"`
	NextTryAt time.Time      `json:"next_try_at"`
	// Held is set once the failures reach the supervisor's limit; Holds
	// counts how often that happened.
	Held  bool `json:"held"`
	Holds int  `json:"holds,omitempty"`
	// HeldAt is when the build was last held, and Reminders how many times
	// the Owner was reminded on the issue since.
	HeldAt    time.Time `json:"held_at,omitzero"`
	Reminders int       `json:"reminders,omitempty"`
	// ResumedBy and ResumedAt name the Owner's last retry of a held build.
	ResumedBy string    `json:"resumed_by,omitempty"`
	ResumedAt time.Time `json:"resumed_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BuildHeldNotice is the payload of a TopicBuildHeld message.
type BuildHeldNotice struct {
	IssueID  core.IssueRef  `json:"issue_id"`
	RunID    core.RunID     `json:"run_id"`
	Attempt  int            `json:"attempt"`
	Failures int            `json:"failures"`
	LastCode core.ErrorCode `json:"last_code"`
	Holds    int            `json:"holds"`
	// Reminder numbers a reminder that the build is still held, from 1;
	// zero on the notice of the hold itself. HeldAt is when it was held.
	Reminder int       `json:"reminder,omitempty"`
	HeldAt   time.Time `json:"held_at,omitzero"`
}

// BuildOutageKey is the key of the outage record of one evaluator attempt.
func BuildOutageKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("build-outage/%s/%d", run, attempt)
}

// ReadBuildOutage reads the outage record of one evaluator attempt and its
// version, NOT_FOUND while its build met none.
func ReadBuildOutage(ctx context.Context, kv state.KV, run core.RunID, attempt int) (BuildOutage, int64, error) {
	entry, err := kv.GetKV(ctx, BuildNamespace, BuildOutageKey(run, attempt))
	if err != nil {
		return BuildOutage{}, 0, err
	}
	var out BuildOutage
	if json.Unmarshal(entry.Value, &out) != nil || out.RunID != run || out.Attempt != attempt {
		return BuildOutage{}, 0, core.NewError(core.CodeTampered, "the stored build outage record cannot be read")
	}
	return out, entry.Version, nil
}

// heldBuildKey is the key of the copy of a held build's outage record that
// the listing of held builds reads. Only held builds have one, so outage
// records of builds that are not held, however many, never crowd a held
// one out of the listing.
func heldBuildKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("build-held/%s/%d", run, attempt)
}

// PutBuildOutage writes the outage record over the given version, 0 for a
// new one, and returns the version written. A held build's record is also
// listed among the held builds; one that is not held is not.
func PutBuildOutage(ctx context.Context, kv state.KV, o BuildOutage, version int64) (int64, error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return 0, core.NewError(core.CodeInternal, "a build outage record cannot be encoded").WithCause(err)
	}
	written, err := kv.PutKV(ctx, state.KVEntry{Namespace: BuildNamespace, Key: BuildOutageKey(o.RunID, o.Attempt), Value: raw, Version: version})
	if err != nil {
		return 0, err
	}
	if o.Held {
		return written.Version, listHeld(ctx, kv, o.RunID, o.Attempt, raw)
	}
	return written.Version, unlistHeld(ctx, kv, o.RunID, o.Attempt)
}

// DeleteBuildOutage removes the outage record of one evaluator attempt,
// read at version, and takes it off the listing of held builds.
func DeleteBuildOutage(ctx context.Context, kv state.KV, run core.RunID, attempt int, version int64) error {
	if err := kv.DeleteKV(ctx, BuildNamespace, BuildOutageKey(run, attempt), version); err != nil {
		return err
	}
	return unlistHeld(ctx, kv, run, attempt)
}

// IndexHeldBuild lists a held build stored by a release that kept no
// listing of held builds. A build that is not held, or already listed, is
// left as it is.
func IndexHeldBuild(ctx context.Context, kv state.KV, o BuildOutage) error {
	if !o.Held {
		return nil
	}
	if listed, err := HeldBuildListed(ctx, kv, o.RunID, o.Attempt); err != nil || listed {
		return err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return core.NewError(core.CodeInternal, "a build outage record cannot be encoded").WithCause(err)
	}
	return listHeld(ctx, kv, o.RunID, o.Attempt, raw)
}

// HeldBuildListed reports whether one evaluator attempt's build is on the
// listing of held builds.
func HeldBuildListed(ctx context.Context, kv state.KV, run core.RunID, attempt int) (bool, error) {
	_, err := kv.GetKV(ctx, BuildNamespace, heldBuildKey(run, attempt))
	switch {
	case err == nil:
		return true, nil
	case core.HasCode(err, core.CodeNotFound):
		return false, nil
	}
	return false, err
}

// listHeld writes the listed copy of a held build's record.
func listHeld(ctx context.Context, kv state.KV, run core.RunID, attempt int, raw []byte) error {
	key := heldBuildKey(run, attempt)
	var version int64
	switch current, err := kv.GetKV(ctx, BuildNamespace, key); {
	case err == nil:
		version = current.Version
	case !core.HasCode(err, core.CodeNotFound):
		return err
	}
	_, err := kv.PutKV(ctx, state.KVEntry{Namespace: BuildNamespace, Key: key, Value: raw, Version: version})
	return err
}

// unlistHeld removes the listed copy of a build's record, if it has one.
func unlistHeld(ctx context.Context, kv state.KV, run core.RunID, attempt int) error {
	key := heldBuildKey(run, attempt)
	current, err := kv.GetKV(ctx, BuildNamespace, key)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil
	case err != nil:
		return err
	}
	return kv.DeleteKV(ctx, BuildNamespace, key, current.Version)
}

// maxListedOutages bounds how many held builds one listing reads.
const maxListedOutages = 500

// ListHeldBuilds returns the held builds, at most 500, for the Owner's
// views. It reads the listing of held builds only.
func ListHeldBuilds(ctx context.Context, kv state.KV) ([]BuildOutage, error) {
	entries, err := kv.ListKV(ctx, BuildNamespace, "build-held/", maxListedOutages)
	if err != nil {
		return nil, err
	}
	var held []BuildOutage
	for _, e := range entries {
		var o BuildOutage
		if json.Unmarshal(e.Value, &o) != nil {
			return nil, core.NewError(core.CodeTampered, "a stored build outage record cannot be read")
		}
		if o.Held {
			held = append(held, o)
		}
	}
	return held, nil
}

// ResumeHeldBuild starts a held build over on the Owner's retry: the
// supervisor tries it at once and counts its outages from zero again. A
// build that is not held is CONFLICT; one with no outage is NOT_FOUND.
func ResumeHeldBuild(ctx context.Context, kv state.KV, run core.RunID, attempt int, owner string, now time.Time) error {
	o, version, err := ReadBuildOutage(ctx, kv, run, attempt)
	if err != nil {
		return err
	}
	if !o.Held {
		return core.NewError(core.CodeConflict, "the build is being retried and is not held")
	}
	o.Held, o.Failures, o.NextTryAt, o.Reminders = false, 0, now, 0
	o.ResumedBy, o.ResumedAt, o.UpdatedAt = owner, now, now
	_, err = PutBuildOutage(ctx, kv, o, version)
	return err
}
