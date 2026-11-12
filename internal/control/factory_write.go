package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ConfirmOwnEdit lists the comments of the issue updated at or after since
// that the factory did not write. It reads GitHub, so it runs outside any
// store transaction.
type ConfirmOwnEdit func(ctx context.Context, ref core.IssueRef, since time.Time) ([]github.Comment, error)

// AcceptReconciledIssue stores a change that reconciliation found on GitHub
// as an inbox entry, the way the webhook route stores a delivery, so the
// same drain hands it to intake. ev carries the issue as GitHub lists it now
// and a delivery ID derived from the issue and its update time, so the same
// change found twice is stored once.
//
// A change the inbox already holds is not stored again: when a webhook
// about the issue at this update time, or a later one, was stored, the
// issue's view records that time and nothing was missed. Nor is the
// factory's own edit (see RecordFactoryWrite): the issue's view takes it in
// without an inbox entry, so intake is not run for what it wrote itself.
//
// GitHub's update times have whole-second precision, so a person's change
// in the same second as the factory's edit leaves the issue at the time the
// edit was recorded with. A change found at that time is taken for the
// factory's own only when its content proves it: the issue still shows what
// the factory saw just after its edit, and confirm finds no comment since
// just before it but the factory's and those taken in already (see
// takenIn). The edit is usually the factory's answer to a person's
// comment, which then leaves the issue at its update time just before the
// edit: that comment is a change taken in, not one to find.
// Anything else, a failure to confirm included, is stored: taking the factory's edit in once more is
// harmless, losing a person's change is not. It reports whether the entry
// was stored.
func AcceptReconciledIssue(ctx context.Context, store state.Store, ev github.WebhookEvent, confirm ConfirmOwnEdit) (bool, error) {
	if ev.Issue == nil || ev.Issue.UpdatedAt.IsZero() || ev.FromSelf {
		return false, core.NewError(core.CodeInvalidArgument, "a reconciled change names an issue and its update time")
	}
	if err := ev.Validate(); err != nil {
		return false, err
	}
	var seen issueProjection
	err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		seen, _, err = readProjection(ctx, tx, ev.Issue.Ref)
		return err
	})
	if err != nil || !ev.Issue.UpdatedAt.After(seen.IssueAt) {
		return false, err
	}
	own, others := false, []github.Comment(nil)
	if ownCandidate(seen, *ev.Issue) && confirm != nil {
		// A second earlier, so the check holds however GitHub rounds since.
		others, err = confirm(ctx, ev.Issue.Ref, seen.SelfSince.Add(-time.Second))
		own = err == nil
	}
	var accepted bool
	err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		current, _, err := readProjection(ctx, tx, ev.Issue.Ref)
		switch {
		case err != nil:
			return err
		case !ev.Issue.UpdatedAt.After(current.IssueAt):
			return nil
		case own && ownCandidate(current, *ev.Issue) && takenIn(current, others):
			return projectEvent(ctx, tx, ev)
		}
		accepted, err = acceptEvent(ctx, tx, ev)
		return err
	})
	return accepted, err
}

// ownCandidate reports whether issue, as listed, stands where the factory's
// recorded edit left it: the same update time and the same content.
func ownCandidate(view issueProjection, issue github.Issue) bool {
	return !view.SelfAt.IsZero() && issue.UpdatedAt.Equal(view.SelfAt) && view.SelfDigest == contentDigest(issue)
}

// takenIn reports whether every comment of others was updated no later
// than the update the issue's view took in. Such a comment was there when
// that update was taken in, and intake read it then; or it came later in
// the same second, which leaves the issue at that update time, so polling
// could not tell it apart from what was taken in whether the factory edited
// the issue after it or not.
func takenIn(view issueProjection, others []github.Comment) bool {
	return !slices.ContainsFunc(others, func(c github.Comment) bool { return c.UpdatedAt.After(view.IssueAt) })
}

// RecordFactoryWrite notes that the factory edited an issue: before is the
// issue as GitHub showed it just before the edit, after just after it, and
// explained says that after differs from before by nothing but what the
// edit did, comments aside. The edit is recognized by a later poll (see
// AcceptReconciledIssue) only when it is explained and before is a change
// already taken in (the issue's view holds it, or it was the factory's own
// previous edit): a person's change whose webhook was lost before the edit
// is then still found, because the issue no longer stands at an update
// time the factory caused alone. An issue with no view yet has nothing
// taken in, and nothing is recorded.
func RecordFactoryWrite(ctx context.Context, store state.Store, before, after github.Issue, explained bool) error {
	ref := after.Ref
	if err := ref.Validate(); err != nil {
		return err
	}
	if before.Ref != ref || before.UpdatedAt.IsZero() || after.UpdatedAt.IsZero() || after.UpdatedAt.Before(before.UpdatedAt) {
		return core.NewError(core.CodeInvalidArgument, "a factory edit needs the issue as it was before and after it, in order")
	}
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		current, version, err := readProjection(ctx, tx, ref)
		if err != nil || version == 0 {
			return err
		}
		next := current
		next.SelfAt, next.SelfDigest, next.SelfSince = time.Time{}, "", time.Time{}
		if explained && (!before.UpdatedAt.After(current.IssueAt) || before.UpdatedAt.Equal(current.SelfAt)) {
			next.SelfAt, next.SelfDigest, next.SelfSince = after.UpdatedAt, contentDigest(after), before.UpdatedAt
		}
		if next.SelfAt.Equal(current.SelfAt) && next.SelfDigest == current.SelfDigest && next.SelfSince.Equal(current.SelfSince) {
			return nil
		}
		value, err := json.Marshal(next)
		if err != nil {
			return core.NewError(core.CodeInternal, "the issue view could not be encoded").WithCause(err)
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: issueViewNamespace, Key: ref.String(), Value: value, Version: version})
		return err
	})
}

// contentDigest condenses what a person can change on an issue besides its
// comments: title, body, state and labels.
func contentDigest(issue github.Issue) string {
	fields := []string{issue.Title, issue.Body, string(issue.State)}
	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l.Name)
	}
	slices.Sort(labels)
	h := sha256.New()
	for _, f := range append(fields, labels...) {
		// Each field is length-prefixed so no two contents share a digest;
		// writing to a hash never fails.
		_, _ = fmt.Fprintf(h, "%d:%s;", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))
}
