package handoff_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The public summary of a held release is chosen by its result and code
// only: the supervisor's reason, which can name units, backends and
// errors, never reaches it.
func TestAReleaseHoldIsSummarisedWithoutItsReason(t *testing.T) {
	t.Parallel()
	secretish := "RELEASE_HOLD: what runs as app at /srv/factory/data cannot be observed"
	for _, tc := range []struct {
		result string
		code   core.ErrorCode
		want   string
	}{
		{"held", core.CodeFrozen, "emergency stop"},
		{"held", core.CodeReleaseHold, "judged and rolled back"},
		{"held", core.CodeConflict, "could not start"},
		{"refused", core.CodeApprovalRequired, "the issue is blocked"},
		{"refused", core.CodeTampered, "refused before anything was deployed"},
	} {
		h := handoff.ReleaseHold{RunID: "run-1", Attempt: 1, Result: tc.result, Code: tc.code, Reason: secretish}
		got := h.Summary()
		if !strings.Contains(got, tc.want) || strings.Contains(got, "/srv") || strings.Contains(got, "what runs as") {
			t.Errorf("%s %s: %q", tc.result, tc.code, got)
		}
		if tc.result == "held" && !strings.Contains(got, "waits in ReleaseCandidate") {
			t.Errorf("%s %s does not say where the issue waits: %q", tc.result, tc.code, got)
		}
	}
}

// A held release, not yet resumed, is resumed by the Owner's retry once. A
// refused hold an earlier release recorded waits for nobody: the
// supervisor judges it again, and a retry resumes it like a held one.
func TestOnlyAHeldReleaseIsResumed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memoryKV{}
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	if err := handoff.ResumeHeldRelease(ctx, kv, "run-1", 1, "owner", now); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("resuming a release with no hold: %v", err)
	}
	if err := handoff.PutReleaseHold(ctx, kv, handoff.ReleaseHold{RunID: "run-1", Attempt: 1, Result: "refused", Code: core.CodeApprovalRequired, HeldAt: now}); err != nil {
		t.Fatal(err)
	}
	refused, err := handoff.ReadReleaseHold(ctx, kv, "run-1", 1)
	if err != nil || refused.Waiting() || !refused.Resumed() {
		t.Fatalf("a refused hold of an earlier release: %+v, %v; want it judged again, waiting for nobody", refused, err)
	}
	if err := handoff.ResumeHeldRelease(ctx, kv, "run-1", 1, "owner", now); err != nil {
		t.Fatalf("a refused hold of an earlier release was not resumed: %v", err)
	}
	if err := handoff.PutReleaseHold(ctx, kv, handoff.ReleaseHold{RunID: "run-1", Attempt: 1, Result: "held", Code: core.CodeFrozen, HeldAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := handoff.ResumeHeldRelease(ctx, kv, "run-1", 1, "owner", now.Add(time.Minute)); err != nil {
		t.Fatalf("resume a held release: %v", err)
	}
	h, err := handoff.ReadReleaseHold(ctx, kv, "run-1", 1)
	if err != nil || h.ResumedBy != "owner" || !h.ResumedAt.Equal(now.Add(time.Minute)) || h.Waiting() {
		t.Fatalf("the resumed hold: %+v, %v", h, err)
	}
	if err := handoff.ResumeHeldRelease(ctx, kv, "run-1", 1, "owner", now.Add(2*time.Minute)); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a release already resumed was resumed again: %v", err)
	}
}
