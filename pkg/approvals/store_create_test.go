package approvals_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestAStoreIsNotBuiltWithoutItsDependencies(t *testing.T) {
	t.Parallel()
	clock := intaketest.NewClock(created)
	keys := intaketest.NewKeys("k")
	full := approvals.Config{State: intaketest.NewState(clock), Signer: keys, Verifier: keys, Audit: &intaketest.AuditLog{}}
	cases := map[string]func(*approvals.Config){
		"no state":     func(c *approvals.Config) { c.State = nil },
		"no signer":    func(c *approvals.Config) { c.Signer = nil },
		"no verifier":  func(c *approvals.Config) { c.Verifier = nil },
		"no audit log": func(c *approvals.Config) { c.Audit = nil },
		"negative step-up age": func(c *approvals.Config) {
			c.StepUpMaxAge = -time.Second
		},
	}
	for name, mutate := range cases {
		cfg := full
		mutate(&cfg)
		if _, err := approvals.NewStore(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
	}
	if _, err := approvals.NewStore(full); err != nil {
		t.Fatalf("a complete configuration was refused: %v", err)
	}
}

func TestARequestIsStoredPendingAndShownInTheInbox(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	stored := f.pending(t, nil)
	if stored.Status != approvals.StatusPending || stored.SubjectDigest != subject {
		t.Fatalf("stored request = %+v", stored)
	}
	got, err := f.store.Get(ctx, "apr-0001")
	if err != nil || got.Nonce != stored.Nonce || got.RiskSummary != stored.RiskSummary || !got.StepUp {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	listed, err := f.store.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusPending}})
	if err != nil || len(listed) != 1 || listed[0].ID != "apr-0001" {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	if got := f.audit.Find("approvals.request", core.OutcomeExecuted); len(got) != 1 || got[0].SubjectDigest != subject {
		t.Fatalf("a stored request must be audited once, got %+v", got)
	}
}

func TestAnUnknownRequestIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.store.Get(ctx, "apr-missing")
	wantCode(t, err, core.CodeNotFound, "Get")
	_, err = f.store.Decision(ctx, "apr-missing")
	wantCode(t, err, core.CodeNotFound, "Decision")
	_, err = f.store.Consume(ctx, "apr-missing", subject, policy)
	wantCode(t, err, core.CodeNotFound, "Consume")
	a := validApproval()
	a.RequestID = "apr-missing"
	_, err = f.store.Decide(asOwner(a), a)
	wantCode(t, err, core.CodeNotFound, "Decide")
	f.pending(t, nil)
	_, err = f.store.Decision(ctx, "apr-0001")
	wantCode(t, err, core.CodeNotFound, "Decision of an undecided request")
}

func TestARepeatedRequestIDIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, nil)
	again := validRequest()
	again.Nonce, again.SubjectDigest = "ffffffffffffffff0000", other
	_, err := f.store.Create(context.Background(), again)
	wantCode(t, err, core.CodeAlreadyExists, "second request with the same id")
	if got, _ := f.store.Get(context.Background(), "apr-0001"); got.SubjectDigest != subject {
		t.Fatalf("the first request was overwritten: %+v", got)
	}
}

func TestANonceCannotBeReusedByAnotherRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, nil)
	reuse := validRequest()
	reuse.ID = "apr-0002"
	_, err := f.store.Create(context.Background(), reuse)
	wantCode(t, err, core.CodeAlreadyExists, "request reusing a nonce")
	_, err = f.store.Get(context.Background(), "apr-0002")
	wantCode(t, err, core.CodeNotFound, "the refused request")
}

func TestOnlyAValidPendingUnexpiredRequestIsStored(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*approvals.Request)
		want   core.ErrorCode
	}{
		"subject not a digest": {func(r *approvals.Request) { r.SubjectDigest = "latest" }, core.CodeInvalidArgument},
		"guessable nonce":      {func(r *approvals.Request) { r.Nonce = "1" }, core.CodeInvalidArgument},
		"never expires":        {func(r *approvals.Request) { r.ExpiresAt = time.Time{} }, core.CodeInvalidArgument},
		"already approved":     {func(r *approvals.Request) { r.Status = approvals.StatusApproved }, core.CodeInvalidArgument},
		"already lapsed": {func(r *approvals.Request) {
			r.CreatedAt, r.ExpiresAt = created.Add(-2*time.Hour), created.Add(-time.Hour)
		}, core.CodeExpired},
	}
	for name, tc := range cases {
		f := newFixture(t)
		r := validRequest()
		tc.mutate(&r)
		if _, err := f.store.Create(context.Background(), r); !core.HasCode(err, tc.want) {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
		if listed, _ := f.store.List(context.Background(), approvals.Filter{}); len(listed) != 0 {
			t.Errorf("%s: a refused request was stored: %+v", name, listed)
		}
	}
}

func TestAPendingRequestReadsAsExpiredOnceItLapses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.pending(t, nil)
	f.clock.Advance(expires.Sub(f.clock.Now()))
	if got := f.status(t); got != approvals.StatusExpired {
		t.Fatalf("status at expiry = %s", got)
	}
	open, _ := f.store.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusPending}})
	lapsed, _ := f.store.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusExpired}})
	if len(open) != 0 || len(lapsed) != 1 {
		t.Fatalf("pending = %d, expired = %d", len(open), len(lapsed))
	}
}

func TestListFiltersByStatusIssueAndLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	otherIssue := core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
	for i, id := range []string{"apr-0002", "apr-0003"} {
		f.pending(t, func(r *approvals.Request) {
			r.ID, r.IssueID = id, otherIssue
			r.Nonce = "0000000000000000" + id
			r.CreatedAt = created.Add(time.Duration(i+1) * time.Second)
		})
	}
	all, err := f.store.List(ctx, approvals.Filter{})
	if err != nil || len(all) != 3 || all[0].ID != "apr-0001" || all[2].ID != "apr-0003" {
		t.Fatalf("List must return every request, oldest first: %+v, %v", all, err)
	}
	approved, _ := f.store.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusApproved}})
	if len(approved) != 1 || approved[0].ID != "apr-0001" {
		t.Fatalf("approved = %+v", approved)
	}
	ofIssue, _ := f.store.List(ctx, approvals.Filter{IssueID: otherIssue})
	if len(ofIssue) != 2 {
		t.Fatalf("requests of the other issue = %+v", ofIssue)
	}
	limited, _ := f.store.List(ctx, approvals.Filter{IssueID: otherIssue, Limit: 1})
	if len(limited) != 1 || limited[0].ID != "apr-0002" {
		t.Fatalf("limited = %+v", limited)
	}
}
