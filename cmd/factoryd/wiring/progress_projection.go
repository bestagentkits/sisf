package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Viewer names the account a GitHub token acts as; github.RESTClient is
// one. An install on the operator's login learns its own account this way.
type Viewer interface {
	Viewer(ctx context.Context) (github.User, error)
}

// progressStates are the states whose issues carry a progress comment:
// from the claim on.
var progressStates = []core.State{
	core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation, core.StateReleaseCandidate, core.StateCanary,
	core.StateMonitoring, core.StateVerified, core.StateClosed, core.StateRolledBack, core.StateBlocked,
}

// projectProgress keeps one progress comment on every issue a run claimed,
// edited in place as the run passes its checkpoints. What was last written
// is remembered per issue, so an issue whose progress did not change costs
// no call to GitHub; after a restart each comment is compared once and
// left alone when it is the same.
func (c *Control) projectProgress(ctx context.Context) error {
	var records []core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		records, err = tx.ListRecords(ctx, state.RecordFilter{States: progressStates})
		return err
	})
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range records {
		if err := c.showProgress(ctx, r); err != nil {
			failed = append(failed, fmt.Errorf("show the progress of %s: %w", r.IssueID, err))
		}
	}
	return errors.Join(failed...)
}

// showProgress writes the progress of one issue when it changed.
func (c *Control) showProgress(ctx context.Context, r core.StateRecord) error {
	if r.RunID == "" {
		return nil
	}
	facts, err := c.progressFacts(ctx, r)
	if err != nil {
		return err
	}
	body := renderProgress(facts)
	if c.redactor != nil {
		body = c.redactor.Secrets(body)
	}
	c.mu.Lock()
	shown := c.progressShown[r.IssueID] == body
	c.mu.Unlock()
	if shown || c.progressStored(ctx, r.IssueID, body) {
		c.mu.Lock()
		c.progressShown[r.IssueID] = body
		c.mu.Unlock()
		return nil
	}
	writer, err := c.progressWriter(ctx)
	if err != nil {
		return err
	}
	err = c.editIssue(ctx, r.IssueID, editIntent{}, func(ctx context.Context) error {
		_, err := writer.Upsert(ctx, r.IssueID, body)
		return err
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.progressShown[r.IssueID] = body
	c.mu.Unlock()
	return c.storeProgress(ctx, r.IssueID, body)
}

// progressNamespace keeps the digest of the progress each issue shows, so
// a restart does not read every issue's comments again to learn it.
const progressNamespace = "progress"

func progressDigest(body string) []byte {
	sum := sha256.Sum256([]byte(body))
	return []byte(hex.EncodeToString(sum[:]))
}

// progressStored reports whether the issue already shows body.
func (c *Control) progressStored(ctx context.Context, issue core.IssueRef, body string) bool {
	stored := false
	_ = c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, progressNamespace, issue.String())
		stored = err == nil && string(entry.Value) == string(progressDigest(body))
		return nil
	})
	return stored
}

// storeProgress records that the issue shows body.
func (c *Control) storeProgress(ctx context.Context, issue core.IssueRef, body string) error {
	return c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		version := int64(0)
		if entry, err := tx.GetKV(ctx, progressNamespace, issue.String()); err == nil {
			version = entry.Version
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: progressNamespace, Key: issue.String(), Value: progressDigest(body), Version: version})
		return err
	})
}

// progressWriter is the progress comment writer, its account learned from
// GitHub the first time when the install names none.
func (c *Control) progressWriter(ctx context.Context) (*github.ProgressComment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress.Self.IsZero() {
		viewer, ok := c.cfg.GitHub.(Viewer)
		if !ok {
			return nil, core.NewError(core.CodeNotSupported, "the factory's own GitHub account is not known, so its progress comment cannot be told apart")
		}
		u, err := viewer.Viewer(ctx)
		if err != nil {
			return nil, err
		}
		c.progress.Self = github.Identity{ID: u.ID, Login: u.Login}
	}
	return c.progress, nil
}

// progressFacts reads what the progress of r is rendered from.
func (c *Control) progressFacts(ctx context.Context, r core.StateRecord) (progressFacts, error) {
	f := progressFacts{record: r}
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		if entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(r.RunID)); err == nil {
			var run dispatcher.RunRecord
			if json.Unmarshal(entry.Value, &run) == nil && run.RunID == r.RunID {
				f.run = &run
			}
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		var err error
		if f.notes, err = handoff.ReadQANotes(ctx, tx, r.RunID); err != nil {
			return err
		}
		if r.State == core.StateQAEvaluation {
			if hold, err := handoff.ReadQAHold(ctx, tx, r.RunID, r.Attempt); err == nil {
				f.hold = &hold
			} else if !core.HasCode(err, core.CodeNotFound) {
				return err
			}
		}
		if pr, err := handoff.ReadCandidatePR(ctx, tx, r.RunID); err == nil {
			f.pr = &pr
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		if entry, err := tx.GetKV(ctx, release.RecoveryNamespace, release.RecoveryKey(r.RunID)); err == nil {
			var report release.RecoveryReport
			if json.Unmarshal(entry.Value, &report) == nil && report.RunID == r.RunID {
				f.restored = report.RestoredTo
			}
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		return nil
	})
	return f, err
}
