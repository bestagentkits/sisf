package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// controlDocuments are documents the control zone keeps, one per namespace
// it owns, as the components of that zone store them.
var controlDocuments = []state.KVEntry{
	{Namespace: "policy", Key: "baseline", Value: []byte(`{"revision":"approved"}`)},
	{Namespace: "install", Key: "budget_confirmation", Value: []byte(`{"mode":"unlimited"}`)},
	{Namespace: "approvals", Key: "request/apr-1", Value: []byte(`{"status":"pending"}`)},
	{Namespace: "intake", Key: "issue/acme/app#7", Value: []byte(`{}`)},
	{Namespace: "control.policy", Key: "refs/abc", Value: []byte(`{}`)},
	{Namespace: "control.issue_view", Key: "acme/app#7", Value: []byte(`{}`)},
}

// agentReadable are the control zone's namespaces the agent zone reads to
// start a run: the budget confirmation, the approved scope and the
// published policy references.
var agentReadable = map[string]bool{"install": true, "intake": true, "control.policy": true}

// seedControlDocuments stores controlDocuments through the control zone.
func seedControlDocuments(t *testing.T, control *sqlstore.Store) {
	t.Helper()
	mustUpdate(t, control, func(tx state.Tx) error {
		for _, doc := range controlDocuments {
			if _, err := tx.PutKV(ctx, doc); err != nil {
				return err
			}
		}
		return nil
	})
}

// documentValue reads one document through the control zone.
func documentValue(t *testing.T, control *sqlstore.Store, namespace, key string) state.KVEntry {
	t.Helper()
	var got state.KVEntry
	if err := view(control, func(tx state.Tx) (err error) {
		got, err = tx.GetKV(ctx, namespace, key)
		return err
	}); err != nil {
		t.Fatalf("read %s/%s: %v", namespace, key, err)
	}
	return got
}

func TestAgentZoneCannotWriteTheControlZonesDocuments(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		seedControlDocuments(t, control)
		agent := db.Open(t, core.ZoneAgent)

		for _, doc := range controlDocuments {
			forged := state.KVEntry{Namespace: doc.Namespace, Key: doc.Key, Value: []byte(`{"forged":true}`), Version: 1}
			wantCode(t, update(agent, func(tx state.Tx) error { _, err := tx.PutKV(ctx, forged); return err }),
				core.CodePermissionDenied)
			created := state.KVEntry{Namespace: doc.Namespace, Key: "planted", Value: []byte(`{}`)}
			wantCode(t, update(agent, func(tx state.Tx) error { _, err := tx.PutKV(ctx, created); return err }),
				core.CodePermissionDenied)
			wantCode(t, update(agent, func(tx state.Tx) error { return tx.DeleteKV(ctx, doc.Namespace, doc.Key, 1) }),
				core.CodePermissionDenied)

			if got := documentValue(t, control, doc.Namespace, doc.Key); string(got.Value) != string(doc.Value) || got.Version != 1 {
				t.Errorf("%s/%s after the refused writes = %q at version %d", doc.Namespace, doc.Key, got.Value, got.Version)
			}
		}
		// A namespace no zone has claimed belongs to the control zone.
		wantCode(t, update(agent, func(tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: "unclaimed", Key: "k"})
			return err
		}), core.CodePermissionDenied)
	})
}

func TestAgentZoneReadsOnlyWhatItNeedsToStartARun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		seedControlDocuments(t, control)
		agent := db.Open(t, core.ZoneAgent)

		for _, doc := range controlDocuments {
			getErr := view(agent, func(tx state.Tx) error { _, err := tx.GetKV(ctx, doc.Namespace, doc.Key); return err })
			listErr := view(agent, func(tx state.Tx) error { _, err := tx.ListKV(ctx, doc.Namespace, "", 0); return err })
			if agentReadable[doc.Namespace] {
				if getErr != nil || listErr != nil {
					t.Errorf("the agent zone cannot read %s: %v, %v", doc.Namespace, getErr, listErr)
				}
				continue
			}
			wantCode(t, getErr, core.CodePermissionDenied)
			wantCode(t, listErr, core.CodePermissionDenied)
		}
	})
}

func TestTheDispatcherNamespaceBelongsToTheAgentZone(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)

		run := state.KVEntry{Namespace: "dispatcher", Key: "run/run-1", Value: []byte(`{"state":"started"}`)}
		mustUpdate(t, agent, func(tx state.Tx) error {
			written, err := tx.PutKV(ctx, run)
			if err != nil {
				return err
			}
			run.Version = written.Version
			_, err = tx.PutKV(ctx, state.KVEntry{Namespace: run.Namespace, Key: run.Key, Value: []byte(`{"state":"done"}`), Version: run.Version})
			return err
		})
		if err := view(agent, func(tx state.Tx) error { _, err := tx.ListKV(ctx, "dispatcher", "run/", 0); return err }); err != nil {
			t.Fatalf("the agent zone cannot list its own run records: %v", err)
		}
		// The control zone reads the run state, to show it, but does not
		// write the agent zone's records.
		if got := documentValue(t, control, run.Namespace, run.Key); string(got.Value) != `{"state":"done"}` {
			t.Fatalf("the control zone reads %q", got.Value)
		}
		wantCode(t, update(control, func(tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: run.Namespace, Key: run.Key, Value: []byte(`{}`), Version: 2})
			return err
		}), core.CodePermissionDenied)
	})
}

func TestAgentZoneCannotMoveAnIssueToReadyWithoutTheOwner(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		ref := issue(1)
		mustTransition(t, control, received(ref))
		mustTransition(t, control, step(ref, core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "scoped"))
		pending := mustTransition(t, control, step(ref, core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending, 2, "asked"))

		forged := step(ref, core.StateApprovalPending, core.EventOwnerApproved, core.StateReady, pending.Version, "forged")
		refused(t, agent, "owner approval written by the agent zone", forged, core.CodePermissionDenied)
	})
}

func TestAgentZoneWritesNoStepOutsideTheRunItWorksOn(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		agent := db.Open(t, core.ZoneAgent)
		application := db.Open(t, core.ZoneApplication)
		run := map[core.Event]bool{
			core.EventClaimed: true, core.EventDevelopmentStarted: true,
			core.EventCandidateSubmitted: true, core.EventQAFailed: true,
		}
		for i, edge := range statemachine.Edges() {
			write := step(issue(100+i), edge.From, edge.Event, edge.To, 1, "forged")
			if edge.From == "" {
				write.ExpectedVersion = 0
			}
			if _, err := transition(application, write); !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("%s from the application zone: want %s, got %v", edge.Event, core.CodePermissionDenied, err)
			}
			if run[edge.Event] {
				continue
			}
			if _, err := transition(agent, write); !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("%s from the agent zone: want %s, got %v", edge.Event, core.CodePermissionDenied, err)
			}
		}
	})
}

func TestAgentZoneDrivesTheRunFromClaimToEvaluation(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		ready := readyRecord(t, control, issue(3))

		claimed := claim(t, agent, ready, "worker-1", "run-1")
		retried := along(t, agent, claimed, core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAFailed)
		evaluating := advance(t, agent, retried, core.EventCandidateSubmitted, func(tr *core.Transition) {
			tr.IdempotencyKey = "candidate/resubmitted"
		})
		if evaluating.State != core.StateQAEvaluation || evaluating.Attempt != 2 {
			t.Fatalf("record after the run = %+v", evaluating)
		}
		// A passing evaluation moves the candidate towards users; it is
		// judged and written by the control zone, not by the run.
		refused(t, agent, "qa pass written by the agent zone", next(t, evaluating, core.EventQAPassed, nil), core.CodePermissionDenied)
		advance(t, control, evaluating, core.EventQAPassed, nil)
	})
}

func TestEveryLifecycleStepHasAWriterAndTheApplicationZoneHasNone(t *testing.T) {
	t.Parallel()
	for _, edge := range statemachine.Edges() {
		if !sqlstore.ZoneMayWrite(core.ZoneControl, edge.Event) && !sqlstore.ZoneMayWrite(core.ZoneAgent, edge.Event) {
			t.Errorf("no zone may write %s", edge.Event)
		}
		if sqlstore.ZoneMayWrite(core.ZoneApplication, edge.Event) {
			t.Errorf("the application zone may write %s", edge.Event)
		}
	}
	for _, event := range []core.Event{
		core.EventIssueReceived, core.EventInfoMissing, core.EventClarificationCompleted, core.EventInfoSufficient,
		core.EventApprovalRequired, core.EventScopePermitted, core.EventOwnerApproved, core.EventApprovalDenied,
		core.EventRetryAuthorized, core.EventQAPassed, core.EventRolloutStarted, core.EventIssueClosed,
	} {
		if sqlstore.ZoneMayWrite(core.ZoneAgent, event) {
			t.Errorf("the agent zone may write %s", event)
		}
	}
}
