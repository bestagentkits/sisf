package actions_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// rename calls widgets.rename as actor with key.
func (f fixture) rename(actor core.Actor, in, key string) (core.ActionResult, error) {
	return f.svc.Invoke(context.Background(), "widgets.rename",
		core.ActionRequest{Actor: actor, Input: input(in), IdempotencyKey: key})
}

func TestARetriedWriteWithItsKeyRunsOnceAndGetsTheFirstAnswer(t *testing.T) {
	f := newFixture(t)
	first, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-1")
	if err != nil {
		t.Fatal(err)
	}
	// The same input, written differently, is the same call.
	again, err := f.rename(f.owner(), `{ "name":"beta", "id":"w1" }`, "k-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.applied() != 1 {
		t.Fatalf("the write ran %d times, want once", f.applied())
	}
	if again.AuditID != first.AuditID || string(again.Output) != string(first.Output) || first.AuditID == "" {
		t.Fatalf("the retry answered %+v, the first call %+v", again, first)
	}
	if len(f.audit.Find("widgets.rename", core.OutcomeExecuted)) != 1 {
		t.Fatal("the retry was audited as a second write")
	}
}

func TestAKeyReusedForAnotherInputIsAConflictAndChangesNothing(t *testing.T) {
	f := newFixture(t)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-1"); err != nil {
		t.Fatal(err)
	}
	_, err := f.rename(f.owner(), `{"id":"w1","name":"gamma"}`, "k-1")
	if typed := asError(t, err, core.CodeConflict); typed.Details["reason"] != "idempotency_key_reused" {
		t.Fatalf("the conflict carries %+v", typed.Details)
	}
	if names, n := f.widgets.snapshot(); n != 1 || names["w1"] != "beta" {
		t.Fatalf("the conflicting call wrote: %v after %d writes", names, n)
	}
}

func TestOneCallersKeyNeverAnswersAnotherCallerOrAction(t *testing.T) {
	f := newFixture(t)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rename(f.maintainer(), `{"id":"w1","name":"beta"}`, "shared"); err != nil {
		t.Fatal(err)
	}
	if f.applied() != 2 {
		t.Fatalf("another caller's call with the same key ran %d writes in all, want 2", f.applied())
	}
}

func TestConcurrentCallsWithOneKeyRunTheWriteOnce(t *testing.T) {
	f := newFixture(t)
	const callers = 16
	results := make([]core.ActionResult, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { results[i], errs[i] = f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-race") })
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if results[i].AuditID != results[0].AuditID {
			t.Fatalf("call %d answered %+v, call 0 %+v", i, results[i], results[0])
		}
	}
	if f.applied() != 1 {
		t.Fatalf("%d concurrent calls with one key wrote %d times", callers, f.applied())
	}
}

func TestAFailedCallFreesItsKeySoTheRetryRuns(t *testing.T) {
	f := newFixture(t)
	_, err := f.rename(f.owner(), `{"id":"w9","name":"beta"}`, "k-1")
	requireCode(t, err, core.CodeNotFound)
	f.widgets.mu.Lock()
	f.widgets.names["w9"] = "alpha"
	f.widgets.mu.Unlock()
	if _, err := f.rename(f.owner(), `{"id":"w9","name":"beta"}`, "k-1"); err != nil {
		t.Fatalf("the retry after a failure: %v", err)
	}
	if f.applied() != 1 {
		t.Fatalf("wrote %d times", f.applied())
	}
}

func TestAKeyLapsesAfterTheRetention(t *testing.T) {
	f := newFixture(t)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-1"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(actions.IdempotencyRetention - time.Second)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-1"); err != nil || f.applied() != 1 {
		t.Fatalf("within the retention: %v, %d writes", err, f.applied())
	}
	f.clock.Advance(time.Second)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"gamma"}`, "k-1"); err != nil || f.applied() != 2 {
		t.Fatalf("after the retention: %v, %d writes", err, f.applied())
	}
}

func TestADryRunOrARefusedCallHoldsNoKey(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Invoke(context.Background(), "widgets.rename", core.ActionRequest{Actor: f.owner(),
		Input: input(`{"id":"w1","name":"other"}`), DryRun: true, IdempotencyKey: "k-1"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.rename(viewer(), `{"id":"w1","name":"other"}`, "k-1")
	requireCode(t, err, core.CodePermissionDenied)
	if _, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, "k-1"); err != nil || f.applied() != 1 {
		t.Fatalf("the first real call: %v, %d writes", err, f.applied())
	}
}

func TestAMalformedKeyIsRefused(t *testing.T) {
	f := newFixture(t)
	for name, key := range map[string]string{"too long": strings.Repeat("k", 257), "control character": "k\n1"} {
		_, err := f.rename(f.owner(), `{"id":"w1","name":"beta"}`, key)
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.applied() != 0 {
		t.Fatal("a call with a malformed key wrote")
	}
}
