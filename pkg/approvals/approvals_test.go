package approvals_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	policy  = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	policy2 = core.CommitSHA("fedcba9876543210fedcba9876543210fedcba98")
	subject = core.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	other   = core.Digest("sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
)

var (
	created = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	expires = created.Add(24 * time.Hour)
)

func validRequest() approvals.Request {
	return approvals.Request{
		ID: "apr-0001", FactoryID: "acme-app", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7},
		Action: "release.publish", SubjectDigest: subject, PolicyRevision: policy,
		Nonce: "b3f1c2d4e5a60718", ExpiresAt: expires, RequestedBy: "service:dispatcher",
		RiskSummary: "Publishes a new feature that adds one public endpoint.",
		Diff:        "+ GET /version", AffectedFeatures: []string{"api"}, StepUp: true,
		Status: approvals.StatusPending, CreatedAt: created,
	}
}

func validApproval() approvals.Approval {
	return approvals.Approval{
		RequestID: "apr-0001", SubjectDigest: subject, PolicyRevision: policy, Nonce: "b3f1c2d4e5a60718",
		Decision: approvals.DecisionApprove, ActorID: "user:owner-1", ActorKind: core.ActorHuman,
		Surface: core.SurfaceDashboard, StepUpAt: created.Add(time.Minute),
		DecidedAt: created.Add(time.Minute), ExpiresAt: expires,
	}
}

func TestDecisionsAndStatusesAreClosedSets(t *testing.T) {
	for _, d := range []approvals.Decision{approvals.DecisionApprove, approvals.DecisionReject, approvals.DecisionRequestChanges} {
		if !d.Valid() {
			t.Errorf("decision %q must be valid", d)
		}
	}
	if approvals.Decision("maybe").Valid() || approvals.Decision("").Valid() {
		t.Error("an unknown decision must be rejected")
	}
	open := map[approvals.Status]bool{approvals.StatusPending: true}
	statuses := approvals.Statuses()
	if len(statuses) != 6 {
		t.Fatalf("Statuses() = %v", statuses)
	}
	for _, s := range statuses {
		if !s.Valid() || s.Open() != open[s] {
			t.Errorf("status %q: valid=%v open=%v", s, s.Valid(), s.Open())
		}
	}
	if approvals.Status("").Valid() || approvals.Status("").Open() {
		t.Error("an unknown status is neither valid nor open")
	}
}

func TestRequestValidation(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]func(*approvals.Request){
		"no id":                 func(r *approvals.Request) { r.ID = "" },
		"no factory":            func(r *approvals.Request) { r.FactoryID = "" },
		"no action":             func(r *approvals.Request) { r.Action = "" },
		"subject not a digest":  func(r *approvals.Request) { r.SubjectDigest = "latest" },
		"policy not a commit":   func(r *approvals.Request) { r.PolicyRevision = "main" },
		"no nonce":              func(r *approvals.Request) { r.Nonce = "" },
		"guessable nonce":       func(r *approvals.Request) { r.Nonce = "1" },
		"never expires":         func(r *approvals.Request) { r.ExpiresAt = time.Time{} },
		"expires before made":   func(r *approvals.Request) { r.ExpiresAt = created },
		"no requester":          func(r *approvals.Request) { r.RequestedBy = "" },
		"no risk summary":       func(r *approvals.Request) { r.RiskSummary = " " },
		"unknown status":        func(r *approvals.Request) { r.Status = "open" },
		"malformed issue":       func(r *approvals.Request) { r.IssueID = core.IssueRef{Owner: "acme"} },
		"creation time missing": func(r *approvals.Request) { r.CreatedAt = time.Time{} },
	}
	for name, mutate := range cases {
		r := validRequest()
		mutate(&r)
		if core.CodeOf(r.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, r.Validate())
		}
	}
	global := validRequest()
	global.IssueID = core.IssueRef{}
	if err := global.Validate(); err != nil {
		t.Fatalf("a request that concerns no single issue is valid: %v", err)
	}
	if validRequest().Expired(expires.Add(-time.Second)) || !validRequest().Expired(expires) {
		t.Fatal("a request is open only before its expiry")
	}
}

func TestApprovalValidation(t *testing.T) {
	if err := validApproval().Validate(); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	cases := map[string]func(*approvals.Approval){
		"no request":          func(a *approvals.Approval) { a.RequestID = "" },
		"unknown decision":    func(a *approvals.Approval) { a.Decision = "ok" },
		"anonymous":           func(a *approvals.Approval) { a.ActorID = "" },
		"unknown actor kind":  func(a *approvals.Approval) { a.ActorKind = "bot" },
		"unknown surface":     func(a *approvals.Approval) { a.Surface = "email" },
		"no nonce":            func(a *approvals.Approval) { a.Nonce = "" },
		"subject not pinned":  func(a *approvals.Approval) { a.SubjectDigest = "" },
		"policy not pinned":   func(a *approvals.Approval) { a.PolicyRevision = "" },
		"undated":             func(a *approvals.Approval) { a.DecidedAt = time.Time{} },
		"negative budget cap": func(a *approvals.Approval) { a.Conditions.BudgetCap = -1 },
	}
	for name, mutate := range cases {
		a := validApproval()
		mutate(&a)
		if core.CodeOf(a.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, a.Validate())
		}
	}
}

func TestApprovalAuthorizesOnlyTheSubjectAndPolicyItWasGivenFor(t *testing.T) {
	now := created.Add(time.Hour)
	if err := validApproval().Authorizes(subject, policy, now); err != nil {
		t.Fatalf("a matching, unexpired approval must authorize: %v", err)
	}
	cases := []struct {
		name    string
		mutate  func(*approvals.Approval)
		subject core.Digest
		policy  core.CommitSHA
		at      time.Time
		want    core.ErrorCode
	}{
		{"another subject", nil, other, policy, now, core.CodeApprovalRequired},
		{"policy changed since", nil, subject, policy2, now, core.CodeApprovalRequired},
		{"empty subject", nil, "", policy, now, core.CodeApprovalRequired},
		{"at expiry", nil, subject, policy, expires, core.CodeExpired},
		{"condition expired first", func(a *approvals.Approval) { a.Conditions.ExpiresAt = created.Add(30 * time.Minute) }, subject, policy, now, core.CodeExpired},
		{"no expiry recorded", func(a *approvals.Approval) { a.ExpiresAt = time.Time{} }, subject, policy, now, core.CodeExpired},
		{"rejected", func(a *approvals.Approval) { a.Decision = approvals.DecisionReject }, subject, policy, now, core.CodePermissionDenied},
		{"changes requested", func(a *approvals.Approval) { a.Decision = approvals.DecisionRequestChanges }, subject, policy, now, core.CodePermissionDenied},
		{"zero value", func(a *approvals.Approval) { *a = approvals.Approval{} }, subject, policy, now, core.CodePermissionDenied},
	}
	for _, tc := range cases {
		a := validApproval()
		if tc.mutate != nil {
			tc.mutate(&a)
		}
		if err := a.Authorizes(tc.subject, tc.policy, tc.at); !core.HasCode(err, tc.want) {
			t.Errorf("%s: want %s, got %v", tc.name, tc.want, err)
		}
	}
}

// approvalMutations changes each field of an approval in turn, keyed by the Go
// field name so a field added later cannot be left out of the payload unseen.
var approvalMutations = map[string]func(*approvals.Approval){
	"RequestID":      func(a *approvals.Approval) { a.RequestID = "apr-0002" },
	"SubjectDigest":  func(a *approvals.Approval) { a.SubjectDigest = other },
	"PolicyRevision": func(a *approvals.Approval) { a.PolicyRevision = policy2 },
	"Nonce":          func(a *approvals.Approval) { a.Nonce = "0000000000000000" },
	"Decision":       func(a *approvals.Approval) { a.Decision = approvals.DecisionReject },
	"ActorID":        func(a *approvals.Approval) { a.ActorID = "user:owner-2" },
	"ActorKind":      func(a *approvals.Approval) { a.ActorKind = core.ActorAgent },
	"Surface":        func(a *approvals.Approval) { a.Surface = core.SurfaceWebMCP },
	"StepUpAt":       func(a *approvals.Approval) { a.StepUpAt = time.Time{} },
	"Reason":         func(a *approvals.Approval) { a.Reason = "looks fine" },
	"Conditions":     func(a *approvals.Approval) { a.Conditions.BudgetCap = 5_000_000 },
	"DecidedAt":      func(a *approvals.Approval) { a.DecidedAt = a.DecidedAt.Add(time.Nanosecond) },
	"ExpiresAt":      func(a *approvals.Approval) { a.ExpiresAt = a.ExpiresAt.Add(time.Nanosecond) },
}

func signedApproval() approvals.Approval {
	a := validApproval()
	a.Signature = core.Signature{KeyID: "approvals-2026-11", Algorithm: "ed25519", Value: []byte{1}}
	return a
}

func TestChangingAnyBoundApprovalFieldChangesTheSigningPayload(t *testing.T) {
	t.Parallel()
	base := signedApproval().SigningPayload()
	fields := reflect.TypeOf(approvals.Approval{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if name == "Signature" {
			continue
		}
		mutate, ok := approvalMutations[name]
		if !ok {
			t.Errorf("approval field %s has no case proving the signature covers it", name)
			continue
		}
		changed := signedApproval()
		mutate(&changed)
		if bytes.Equal(base, changed.SigningPayload()) {
			t.Errorf("changing %s left the signing payload unchanged", name)
		}
	}
	narrowed := signedApproval()
	narrowed.Conditions.ExpiresAt = created.Add(30 * time.Minute)
	if bytes.Equal(base, narrowed.SigningPayload()) {
		t.Error("an earlier expiry condition left the signing payload unchanged")
	}
}

func TestTheApprovalPayloadIsStableAndLeavesTheSignatureOut(t *testing.T) {
	t.Parallel()
	approval := signedApproval()
	base := approval.SigningPayload()
	if !bytes.Equal(base, signedApproval().SigningPayload()) {
		t.Fatal("two equal approvals produced different payloads")
	}
	if !strings.HasPrefix(string(base), "sisf-signed-record\ndomain=13:sisf.approval\nversion=1:1\n") {
		t.Fatalf("payload does not open with the approval domain and version: %q", base)
	}
	approval.Signature = core.Signature{KeyID: "another-key", Algorithm: "ed25519", Value: []byte{9}}
	if !bytes.Equal(base, approval.SigningPayload()) {
		t.Fatal("the payload depends on the signature it is signed with")
	}
	approval.DecidedAt = approval.DecidedAt.In(time.FixedZone("ICT", 7*60*60))
	if !bytes.Equal(base, approval.SigningPayload()) {
		t.Fatal("the payload depends on the zone an instant is expressed in")
	}
}

func TestAnApprovalSignatureDoesNotCoverAnotherRecordType(t *testing.T) {
	t.Parallel()
	// A decision and a QA receipt never share a payload, so a key that signs
	// one cannot be tricked into vouching for the other.
	approval := signedApproval().SigningPayload()
	receipt := core.QAReceipt{}.SigningPayload()
	if bytes.Equal(approval, receipt) || bytes.HasPrefix(approval, receipt) || bytes.HasPrefix(receipt, approval) {
		t.Fatal("an approval payload and a receipt payload overlap")
	}
	if !bytes.Contains(receipt, []byte("domain=15:sisf.qa_receipt\n")) {
		t.Fatalf("the receipt payload names no domain of its own: %q", receipt)
	}
}

// storeStub proves the contract can be met with the exported types alone and
// shows the single-use rule a real store must enforce.
type storeStub struct {
	request  approvals.Request
	approval approvals.Approval
}

func (s *storeStub) Create(_ context.Context, r approvals.Request) (approvals.Request, error) {
	if err := r.Validate(); err != nil {
		return approvals.Request{}, err
	}
	s.request = r
	return r, nil
}
func (s *storeStub) Get(context.Context, string) (approvals.Request, error) { return s.request, nil }
func (s *storeStub) List(context.Context, approvals.Filter) ([]approvals.Request, error) {
	return []approvals.Request{s.request}, nil
}
func (s *storeStub) Decide(_ context.Context, a approvals.Approval) (approvals.Request, error) {
	switch {
	case !s.request.Status.Open():
		return approvals.Request{}, core.NewError(core.CodeReplayed, "the request was already decided")
	case a.Nonce != s.request.Nonce:
		return approvals.Request{}, core.NewError(core.CodeReplayed, "nonce does not belong to this request")
	case s.request.StepUp && (a.ActorKind != core.ActorHuman || a.StepUpAt.IsZero()):
		return approvals.Request{}, core.NewError(core.CodeStepUpRequired, "a person must confirm this approval")
	}
	s.request.Status, s.approval = approvals.StatusApproved, a
	return s.request, nil
}
func (s *storeStub) Decision(context.Context, string) (approvals.Approval, error) {
	return s.approval, nil
}
func (s *storeStub) Consume(_ context.Context, _ string, sub core.Digest, pol core.CommitSHA) (approvals.Approval, error) {
	if s.request.Status == approvals.StatusConsumed {
		return approvals.Approval{}, core.NewError(core.CodeReplayed, "the approval was already used")
	}
	if err := s.approval.Authorizes(sub, pol, created.Add(time.Hour)); err != nil {
		return approvals.Approval{}, err
	}
	s.request.Status = approvals.StatusConsumed
	return s.approval, nil
}

func TestStoreContractIsImplementableAndAnApprovalIsSingleUse(t *testing.T) {
	ctx := context.Background()
	var store approvals.Store = &storeStub{}
	if _, err := store.Create(ctx, validRequest()); err != nil {
		t.Fatal(err)
	}
	ambient := validApproval()
	ambient.ActorKind, ambient.StepUpAt = core.ActorAgent, time.Time{}
	if _, err := store.Decide(ctx, ambient); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("an agent riding an Owner session must not approve a sensitive request, got %v", err)
	}
	if _, err := store.Decide(ctx, validApproval()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, validApproval()); !core.HasCode(err, core.CodeReplayed) {
		t.Fatalf("deciding twice must be refused, got %v", err)
	}
	if _, err := store.Consume(ctx, "apr-0001", other, policy); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("an approval must not be spent on another subject, got %v", err)
	}
	if _, err := store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(ctx, "apr-0001", subject, policy); !core.HasCode(err, core.CodeReplayed) {
		t.Fatalf("using an approval twice must be refused, got %v", err)
	}
}
