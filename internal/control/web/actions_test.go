package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var approvalLink = regexp.MustCompile(`href="/approvals/([^"]+)"`)

func stopForm(reason string) url.Values { return url.Values{"reason": {reason}} }

func TestStopGoesThroughPlanApprovalAndExecute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	b := e.browser(t)
	b.signIn(t, ownerSub)

	// Asking to stop files a request; nothing is stopped yet.
	plan := b.post(t, "/actions/factory.emergency_stop", stopForm("runaway spend"))
	if plan.status != http.StatusForbidden || !strings.Contains(plan.body, `data-error-code="APPROVAL_REQUIRED"`) {
		t.Fatalf("plan = %d: %.400s", plan.status, plan.body)
	}
	if n := e.seen.ran("dashboard"); n != 0 {
		t.Fatalf("the stop ran %d times before any approval", n)
	}
	m := approvalLink.FindStringSubmatch(plan.body)
	if m == nil {
		t.Fatalf("the result links to no approval request: %.400s", plan.body)
	}
	id, _ := url.PathUnescape(m[1])

	// The Owner reads the request: risk summary, diff, conditions.
	page := b.get(t, "/approvals/"+m[1])
	for _, want := range []string{`data-field="risk-summary"`, `data-field="diff"`, "factory/stop:", `name="budget_cap"`, `name="expires_at"`, "approvals.decide"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("approval page lacks %q", want)
		}
	}
	if list := b.get(t, "/approvals?status=pending"); !strings.Contains(list.body, `data-approval="`+id+`"`) {
		t.Errorf("the pending list does not show the request")
	}

	// A decision without a fresh confirmation needs the step-up.
	decision := url.Values{"request_id": {id}, "decision": {"approve"}, "reason": {"agreed"}, "return_to": {"/approvals/" + m[1]}}
	fillBinding(t, page.body, decision)
	if r := b.post(t, "/actions/approvals.decide", decision); r.status != http.StatusForbidden || !strings.Contains(r.body, `data-error-code="STEP_UP_REQUIRED"`) ||
		!strings.Contains(r.body, "/auth/step-up/google") {
		t.Fatalf("decision without step-up = %d: %.400s", r.status, r.body)
	}
	b.stepUp(t, ownerSub)
	decision.Set("budget_cap", "5000000")
	decision.Set("expires_at", "2026-11-12T09:00:00Z")
	if r := b.post(t, "/actions/approvals.decide", decision); r.status != http.StatusOK || !strings.Contains(r.body, `data-state="done"`) {
		t.Fatalf("decision = %d: %s", r.status, r.body)
	}
	if after := b.get(t, "/approvals/"+m[1]); !strings.Contains(after.body, `data-field="decision"`) || !strings.Contains(after.body, "5.000000") {
		t.Errorf("the decision and its condition are not shown: %.600s", after.body)
	}

	// Approved, the same stop now executes, on the dashboard surface.
	form := stopForm("runaway spend")
	form.Set("approval_id", id)
	if r := b.post(t, "/actions/factory.emergency_stop", form); r.status != http.StatusOK || !strings.Contains(r.body, `data-state="done"`) {
		t.Fatalf("execute = %d: %.400s", r.status, r.body)
	}
	if n := e.seen.ran("dashboard"); n != 1 {
		t.Errorf("the stop ran %d times on the dashboard surface, want once", n)
	}
	// The approval is spent: the same call again is refused.
	if r := b.post(t, "/actions/factory.emergency_stop", form); r.status == http.StatusOK {
		t.Errorf("a spent approval ran the stop again")
	}
}

// fillBinding copies the nonce, digest and policy revision the page shows
// into the decision form, as the browser would submit them.
func fillBinding(t *testing.T, page string, form url.Values) {
	t.Helper()
	for _, field := range []string{"nonce", "subject_digest", "policy_revision"} {
		m := regexp.MustCompile(`name="` + field + `" value="([^"]+)"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("the approval page has no %s field", field)
		}
		form.Set(field, m[1])
	}
}

// errorBody reads the typed error of a REST answer.
type errorBody struct {
	Error struct {
		Code        string `json:"code"`
		Message     string `json:"message"`
		NextActions []struct {
			Action string `json:"action"`
		} `json:"next_actions"`
	} `json:"error"`
}

func TestViewerSeesThePageButTheStopIsDeniedLikeREST(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	b := e.browser(t)
	b.signIn(t, viewerSub)
	if page := b.get(t, "/"); page.status != http.StatusOK || !strings.Contains(page.body, `action="/actions/factory.emergency_stop"`) {
		t.Fatalf("the viewer cannot see the overview: %d", page.status)
	}
	page := b.post(t, "/actions/factory.emergency_stop", stopForm("trying"))
	rest := b.do(t, http.MethodPost, "/api/v1/actions/factory.emergency_stop", `{"input":{"reason":"trying"}}`,
		map[string]string{"Content-Type": "application/json", "X-CSRF-Token": b.csrf(t)})
	var want errorBody
	if err := json.Unmarshal([]byte(rest.body), &want); err != nil {
		t.Fatalf("REST answer: %v: %s", err, rest.body)
	}
	if want.Error.Code != "PERMISSION_DENIED" || page.status != rest.status {
		t.Fatalf("page %d, REST %d %s", page.status, rest.status, want.Error.Code)
	}
	for _, shown := range []string{`data-error-code="` + want.Error.Code + `"`, htmlEscape(want.Error.Message), `data-next-action="` + want.Error.NextActions[0].Action + `"`} {
		if !strings.Contains(page.body, shown) {
			t.Errorf("the page does not show %q as REST returns it", shown)
		}
	}
	if n := e.seen.ran("dashboard") + e.seen.ran("api"); n != 0 {
		t.Errorf("a denied action ran %d times", n)
	}
	// An approval decision is the Owner's alone, on this surface too.
	if r := b.post(t, "/actions/approvals.decide", url.Values{"request_id": {"x"}, "nonce": {"n"}, "subject_digest": {"d"}, "policy_revision": {"p"}, "decision": {"approve"}}); r.status != http.StatusForbidden {
		t.Errorf("a viewer's decision = %d, want 403", r.status)
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

func TestFormPostWithoutValidCSRFTokenIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	b := e.browser(t)
	b.signIn(t, ownerSub)
	for name, form := range map[string]url.Values{
		"missing": stopForm("x"),
		"wrong":   {"reason": {"x"}, "csrf_token": {"not-the-token"}},
	} {
		r := b.postRaw(t, "/actions/factory.emergency_stop", form)
		if r.status != http.StatusForbidden || !strings.Contains(r.body, `data-error-code="PERMISSION_DENIED"`) {
			t.Errorf("%s token: %d: %.300s", name, r.status, r.body)
		}
	}
	if n := len(e.seen.calls); n != 0 {
		t.Errorf("%d handler calls after refused forms", n)
	}
}

func TestOnlyTheOfferedFormsCanBePosted(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	if r := b.post(t, "/actions/budget.set", url.Values{"mode": {"unlimited"}}); r.status != http.StatusNotFound {
		t.Errorf("a form for an action the dashboard does not offer = %d, want 404", r.status)
	}
	if r := b.post(t, "/actions/factory.emergency_stop", url.Values{}); r.status != http.StatusBadRequest {
		t.Errorf("a stop without a reason = %d, want 400", r.status)
	}
}

func TestActionsNotSupportedShowAHonestState(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	// The rollback is only a stand-in in this factory.
	r := b.post(t, "/actions/deployments.rollback", stopForm("bad release"))
	if r.status != http.StatusNotImplemented || !strings.Contains(r.body, `data-error-code="NOT_SUPPORTED"`) || !strings.Contains(r.body, `data-next-action=`) {
		t.Errorf("rollback = %d: %.400s", r.status, r.body)
	}
	for path, wants := range map[string][]string{
		"/product":   {`data-section="docs.list"`, `data-section="changelog.list"`},
		"/telemetry": {`data-state="unavailable"`},
	} {
		page := b.get(t, path)
		for _, want := range wants {
			if !strings.Contains(page.body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if !strings.Contains(page.body, "Not available yet.") {
			t.Errorf("%s shows no 'not available yet' state", path)
		}
	}
}
