package handoff_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// memoryKV is a versioned in-memory KV.
type memoryKV map[string]state.KVEntry

func (m memoryKV) GetKV(_ context.Context, ns, key string) (state.KVEntry, error) {
	e, ok := m[ns+"\x00"+key]
	if !ok {
		return e, core.NewError(core.CodeNotFound, "no entry")
	}
	return e, nil
}

func (m memoryKV) PutKV(_ context.Context, e state.KVEntry) (state.KVEntry, error) {
	if m[e.Namespace+"\x00"+e.Key].Version != e.Version {
		return e, core.NewError(core.CodeConflict, "stale version")
	}
	e.Version++
	m[e.Namespace+"\x00"+e.Key] = e
	return e, nil
}

func (m memoryKV) ListKV(context.Context, string, string, int) ([]state.KVEntry, error) {
	return nil, nil
}
func (m memoryKV) DeleteKV(context.Context, string, string, int64) error { return nil }

// A patch is read back only as the run and attempt it was stored for, and
// one larger than the supervisor builds is refused as tampered.
func TestPatchesAreReadOnlyAsTheRunAndAttemptTheyWereStoredFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memoryKV{}
	doc := handoff.PatchDoc{RunID: "run-1", Attempt: 2, AgentAttempt: 1, Diff: "--- a/x\n+++ b/x\n"}
	if err := handoff.PutDoc(ctx, kv, handoff.Namespace, handoff.PatchKey("run-1", 2), doc); err != nil {
		t.Fatal(err)
	}
	// Writing the same document again keeps it.
	if err := handoff.PutDoc(ctx, kv, handoff.Namespace, handoff.PatchKey("run-1", 2), doc); err != nil {
		t.Fatal(err)
	}
	if got, err := handoff.ReadPatch(ctx, kv, "run-1", 2); err != nil || got.Diff != doc.Diff {
		t.Fatalf("ReadPatch = %+v, %v", got, err)
	}
	if _, err := handoff.ReadPatch(ctx, kv, "run-1", 1); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("an absent attempt read as %v", err)
	}

	moved := doc
	moved.RunID = "run-2"
	if err := handoff.PutDoc(ctx, kv, handoff.Namespace, handoff.PatchKey("run-1", 3), moved); err != nil {
		t.Fatal(err)
	}
	if _, err := handoff.ReadPatch(ctx, kv, "run-1", 3); !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("a patch of another run was accepted: %v", err)
	}
	huge := doc
	huge.Attempt, huge.Diff = 4, strings.Repeat("+", 1<<20+1)
	if err := handoff.PutDoc(ctx, kv, handoff.Namespace, handoff.PatchKey("run-1", 4), huge); err != nil {
		t.Fatal(err)
	}
	if _, err := handoff.ReadPatch(ctx, kv, "run-1", 4); !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("an oversized patch was accepted: %v", err)
	}
}

// The suite digest changes with the policy or the review criteria, so a
// candidate built under another policy is never judged as current.
func TestTheSuiteDigestFollowsThePolicyAndTheReviewCriteria(t *testing.T) {
	t.Parallel()
	base := core.PolicyRefs{PolicySHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReviewSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	otherPolicy, otherReview := base, base
	otherPolicy.PolicySHA = "cccccccccccccccccccccccccccccccccccccccc"
	otherReview.ReviewSHA = "cccccccccccccccccccccccccccccccccccccccc"
	d := handoff.SuiteDigest(base)
	if d == handoff.SuiteDigest(otherPolicy) || d == handoff.SuiteDigest(otherReview) || d != handoff.SuiteDigest(base) {
		t.Fatal("the suite digest does not follow the policy")
	}
}
