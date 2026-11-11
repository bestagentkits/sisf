package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// progressNamespace keeps, for each issue whose run has a progress comment
// that has not shown the run's end yet, the digest of what it shows and
// how its writing fails, so a restart does not read every issue's
// comments again and an ended issue is dropped once shown.
const progressNamespace = "progress"

// progressEntry is what the control zone keeps of one issue's progress.
type progressEntry struct {
	Issue    core.IssueRef `json:"issue"`
	Digest   string        `json:"digest,omitempty"`
	Failures int           `json:"failures,omitempty"`
	RetryAt  time.Time     `json:"retry_at,omitzero"`
}

func decodeProgress(e state.KVEntry) (progressEntry, bool) {
	var entry progressEntry
	if json.Unmarshal(e.Value, &entry) != nil || entry.Issue.String() != e.Key {
		return progressEntry{}, false
	}
	return entry, true
}

func progressDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (c *Control) progressEntry(ctx context.Context, issue core.IssueRef) (progressEntry, bool, error) {
	var entry progressEntry
	found := false
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		e, err := tx.GetKV(ctx, progressNamespace, issue.String())
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		entry, found = decodeProgress(e)
		return nil
	})
	return entry, found, err
}

// storeProgress records the issue's progress entry.
func (c *Control) storeProgress(ctx context.Context, entry progressEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		version := int64(0)
		if e, err := tx.GetKV(ctx, progressNamespace, entry.Issue.String()); err == nil {
			if string(e.Value) == string(raw) {
				return nil
			}
			version = e.Version
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: progressNamespace, Key: entry.Issue.String(), Value: raw, Version: version})
		return err
	})
}

// dropProgress forgets the issue's progress entry: its run ended and the
// end is shown, or given up.
func (c *Control) dropProgress(ctx context.Context, issue core.IssueRef) error {
	return c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		e, err := tx.GetKV(ctx, progressNamespace, issue.String())
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.DeleteKV(ctx, progressNamespace, issue.String(), e.Version)
	})
}
