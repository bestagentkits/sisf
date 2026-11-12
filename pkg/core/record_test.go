package core

import (
	"testing"
	"time"
)

func validRecord() StateRecord {
	return StateRecord{
		FactoryID:      "acme-factory",
		IssueID:        IssueRef{Owner: "acme", Repo: "app", Number: 7},
		State:          StateClaimed,
		Version:        3,
		IdempotencyKey: "delivery-1:claimed",
		LeaseOwner:     "dispatcher-1",
		FencingToken:   5,
		Attempt:        1,
		RunID:          "run-0001",
		UpdatedAt:      time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC),
	}
}

func TestStateRecordValidation(t *testing.T) {
	if err := validRecord().Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	cases := map[string]func(*StateRecord){
		"missing factory":          func(r *StateRecord) { r.FactoryID = "" },
		"missing issue":            func(r *StateRecord) { r.IssueID = IssueRef{} },
		"unknown state":            func(r *StateRecord) { r.State = "Releasing" },
		"version below one":        func(r *StateRecord) { r.Version = 0 },
		"no idempotency key":       func(r *StateRecord) { r.IdempotencyKey = "" },
		"negative attempt":         func(r *StateRecord) { r.Attempt = -1 },
		"negative fencing":         func(r *StateRecord) { r.FencingToken = -1 },
		"fenced without owner":     func(r *StateRecord) { r.LeaseOwner = "" },
		"malformed run id":         func(r *StateRecord) { r.RunID = "Run 1" },
		"missing write instant":    func(r *StateRecord) { r.UpdatedAt = time.Time{} },
		"candidate named by a tag": func(r *StateRecord) { r.CandidateDigest = "latest" },
		"release named by a tag":   func(r *StateRecord) { r.ReleaseDigest = "v1.2.3" },
	}
	for name, mutate := range cases {
		rec := validRecord()
		mutate(&rec)
		if CodeOf(rec.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, rec.Validate())
		}
	}
}

func TestAStateRecordKeepsTheCandidateAndTheReleaseOfItsRun(t *testing.T) {
	rec := validRecord()
	rec.State = StateMonitoring
	rec.CandidateDigest = testDigest
	rec.ReleaseDigest = ReleaseDigest(testDigest)
	if err := rec.Validate(); err != nil {
		t.Fatalf("a record that names its candidate and release was rejected: %v", err)
	}
}

func validTransition() Transition {
	return Transition{
		IssueID:         IssueRef{Owner: "acme", Repo: "app", Number: 7},
		From:            StateReady,
		To:              StateClaimed,
		Event:           EventClaimed,
		ExpectedVersion: 2,
		IdempotencyKey:  "claim:run-0001",
		LeaseOwner:      "dispatcher-1",
		FencingToken:    5,
		RunID:           "run-0001",
		Attempt:         1,
		ActorID:         "service:dispatcher",
	}
}

func TestTransitionValidation(t *testing.T) {
	if err := validTransition().Validate(); err != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}
	cases := map[string]func(*Transition){
		"missing issue":            func(tr *Transition) { tr.IssueID = IssueRef{} },
		"unknown source state":     func(tr *Transition) { tr.From = "Merged" },
		"unknown target state":     func(tr *Transition) { tr.To = "" },
		"unknown event":            func(tr *Transition) { tr.Event = "merged" },
		"negative version":         func(tr *Transition) { tr.ExpectedVersion = -1 },
		"no idempotency key":       func(tr *Transition) { tr.IdempotencyKey = "" },
		"fenced without owner":     func(tr *Transition) { tr.LeaseOwner = "" },
		"negative fencing token":   func(tr *Transition) { tr.FencingToken = -1 },
		"negative attempt":         func(tr *Transition) { tr.Attempt = -1 },
		"no source on later edge":  func(tr *Transition) { tr.From = "" },
		"candidate named by a tag": func(tr *Transition) { tr.CandidateDigest = "latest" },
		"release named by a tag":   func(tr *Transition) { tr.ReleaseDigest = "v1.2.3" },
	}
	for name, mutate := range cases {
		tr := validTransition()
		mutate(&tr)
		if CodeOf(tr.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, tr.Validate())
		}
	}
}

func TestATransitionMayNameTheCandidateAndTheReleaseItIsAbout(t *testing.T) {
	tr := validTransition()
	tr.CandidateDigest = testDigest
	tr.ReleaseDigest = ReleaseDigest(testDigest)
	if err := tr.Validate(); err != nil {
		t.Fatalf("a transition bound to a candidate and a release was rejected: %v", err)
	}
}

func TestOnlyIssueReceiptMayCreateARecord(t *testing.T) {
	create := Transition{
		IssueID:        IssueRef{Owner: "acme", Repo: "app", Number: 7},
		To:             StateIntake,
		Event:          EventIssueReceived,
		IdempotencyKey: "delivery-1",
	}
	if err := create.Validate(); err != nil {
		t.Fatalf("record creation rejected: %v", err)
	}
	if !create.Creates() {
		t.Error("a transition without a source state creates the record")
	}
	create.ExpectedVersion = 1
	if create.Validate() == nil {
		t.Error("creation must expect version zero")
	}
	skip := create
	skip.ExpectedVersion = 0
	skip.To = StateReady
	if skip.Validate() == nil {
		t.Error("a record may only be created in Intake")
	}
	if validTransition().Creates() {
		t.Error("a transition with a source state does not create the record")
	}
}
