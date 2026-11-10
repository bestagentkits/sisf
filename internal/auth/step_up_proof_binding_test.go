package auth_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A confirmation made for one request decides that request once and
// nothing else: not another request, not another action, not twice.
func TestAStepUpIsSpentByTheOneCallItNames(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	stepUpFor(t, b, "owner-sub", csrf, "?action=approvals.decide&target=req-A")
	ctx := context.Background()

	for name, c := range map[string]struct{ action, target string }{
		"another request": {"approvals.decide", "req-B"},
		"another action":  {"deployments.rollback", "req-A"},
		"token minting":   {auth.TokenStepUpAction, ""},
	} {
		if _, err := e.svc.ConsumeStepUp(ctx, view.SessionID, c.action, c.target); !core.HasCode(err, core.CodeStepUpRequired) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if status, _, _ := mint(t, b, csrf, `{"scopes":["prs:write"]}`); status != http.StatusForbidden {
		t.Fatalf("a confirmation of a decision minted a token: %d", status)
	}
	if _, err := e.svc.ConsumeStepUp(ctx, view.SessionID, "approvals.decide", "req-A"); err != nil {
		t.Fatalf("the confirmed call: %v", err)
	}
	if _, err := e.svc.ConsumeStepUp(ctx, view.SessionID, "approvals.decide", "req-A"); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("a confirmation was spent twice: %v", err)
	}
}

func TestAStepUpLapsesQuickly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	stepUpFor(t, b, "owner-sub", map[string]string{auth.CSRFHeader: view.CSRFToken}, "?action=approvals.decide&target=req-A")
	e.clock.Advance(5*time.Minute + time.Second)
	if _, err := e.svc.ConsumeStepUp(context.Background(), view.SessionID, "approvals.decide", "req-A"); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("a lapsed confirmation was accepted: %v", err)
	}
}

// A confirmation belongs to the session that made it, and a step-up must
// name a well-formed call.
func TestAStepUpIsHeldByItsSessionAndNamesAWellFormedCall(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	owner := e.browser(t)
	owner.signIn(t, "google", "owner-sub")
	view, _ := owner.session(t)
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	for _, q := range []string{"?action=approvals.decide", "?target=req-A", "?action=DROP&target=req-A",
		"?action=approvals.decide&target=%09req", "?action=approvals.decide&target=-req"} {
		if resp := owner.do(t, http.MethodPost, "/auth/step-up/google"+q, "", csrf); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("step-up %s: %d", q, resp.StatusCode)
		}
	}
	stepUpFor(t, owner, "owner-sub", csrf, "?action=approvals.decide&target=req-A")
	other := e.browser(t)
	other.signIn(t, "google", "owner-sub")
	otherView, _ := other.session(t)
	if _, err := e.svc.ConsumeStepUp(context.Background(), otherView.SessionID, "approvals.decide", "req-A"); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("another session spent the confirmation: %v", err)
	}
	if _, err := e.svc.ConsumeStepUp(context.Background(), "pat_unknown", "approvals.decide", "req-A"); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("an unknown handle: %v", err)
	}
}
