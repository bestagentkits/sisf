package acceptance

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const lkgDigest core.Digest = "sha256:abababababababababababababababababababababababababababababababab"

// proposal reads approvals.get's answer.
func proposal(t *testing.T, o outcome) approvalactions.Proposal {
	t.Helper()
	if o.Code != "" {
		t.Fatalf("approvals.get answered %s: %s", o.Code, o.Message)
	}
	var p approvalactions.Proposal
	if err := json.Unmarshal(o.Output, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// openRequest finds the one pending request for action.
func openRequest(t *testing.T, c *clients, action string) approvals.Request {
	t.Helper()
	out := c.on(t, surfaceREST, "approvals.list", call{Input: raw(`{}`)})
	var list approvalactions.RequestList
	if err := json.Unmarshal(out.Output, &list); err != nil {
		t.Fatalf("approvals.list: %s %v", out.Code, err)
	}
	for _, r := range list.Requests {
		if r.Action == action && r.Status == approvals.StatusPending {
			return r
		}
	}
	t.Fatalf("no pending request for %s in %+v", action, list.Requests)
	return approvals.Request{}
}

func decideInput(r approvals.Request) json.RawMessage {
	in, _ := json.Marshal(map[string]string{"request_id": r.ID, "nonce": r.Nonce, "subject_digest": string(r.SubjectDigest),
		"policy_revision": string(r.PolicyRevision), "decision": string(approvals.DecisionApprove)})
	return in
}

// An approval request filed on one surface is the same request on every
// other; a token can never decide it, the dashboard can after a step-up,
// and the decision is then visible everywhere and spent once.
func TestAnApprovalCrossesEverySurfaceAndOnlyAFreshSessionDecidesIt(t *testing.T) {
	f := harness.New(t, harness.Options{})
	f.Supervisor.SetLKG("web", lkgDigest)
	owner := f.SignInOwner()
	c := newClients(t, f, owner)

	// Filed from the dashboard.
	planned := c.on(t, surfaceDashboard, "deployments.rollback", call{Input: raw(`{"reason":"the release broke sign-in"}`)})
	if planned.Code != core.CodeApprovalRequired {
		t.Fatalf("a rollback without an approval answered %q: %s", planned.Code, planned.Message)
	}
	req := openRequest(t, c, "deployments.rollback")

	// Visible on every surface.
	get := call{Input: raw(`{"id":"` + req.ID + `"}`)}
	for _, surface := range []string{surfaceCLI, surfaceREST, surfaceMCP, surfaceWebMCP} {
		if p := proposal(t, c.on(t, surface, "approvals.get", get)); p.Request.ID != req.ID || p.Decision != nil {
			t.Errorf("%s shows %+v", surface, p)
		}
	}
	page := owner.Do(http.MethodGet, "/approvals/"+req.ID, nil, nil)
	if body, _ := io.ReadAll(page.Body); page.StatusCode != http.StatusOK || !strings.Contains(string(body), req.ID) {
		t.Errorf("the dashboard's approval page answered %d", page.StatusCode)
	}

	// No token carries a step-up: MCP and a personal access token are
	// refused and pointed at the dashboard.
	for _, surface := range []string{surfaceMCP, surfaceREST, surfaceCLI} {
		got := c.on(t, surface, "approvals.decide", call{Input: decideInput(req)})
		if got.Code != core.CodeStepUpRequired {
			t.Fatalf("%s decided an approval with a token: %q %s", surface, got.Code, got.Message)
		}
		if surface != surfaceCLI && (len(got.Next) == 0 || got.Next[0].Params["dashboard"] != "/approvals") {
			t.Errorf("%s's refusal does not point at the dashboard: %+v", surface, got.Next)
		}
	}

	// The dashboard decides after the step-up the Owner just made.
	if got := c.on(t, surfaceDashboard, "approvals.decide", call{Input: decideInput(req)}); got.Code != "" {
		t.Fatalf("the dashboard could not decide after a step-up: %q %s", got.Code, got.Message)
	}
	for _, surface := range []string{surfaceREST, surfaceCLI} {
		p := proposal(t, c.on(t, surface, "approvals.get", get))
		if p.Decision == nil || p.Decision.Decision != approvals.DecisionApprove {
			t.Errorf("%s does not show the decision: %+v", surface, p.Decision)
		}
	}

	// The rollback runs under the approval; the supervisor spends it.
	run := c.on(t, surfaceWebMCP, "deployments.rollback", call{Input: raw(`{"reason":"the release broke sign-in"}`), ApprovalID: req.ID})
	if run.Code != "" {
		t.Fatalf("the approved rollback answered %q: %s", run.Code, run.Message)
	}
	if _, rollbacks := f.Supervisor.Recorded(); len(rollbacks) != 1 || rollbacks[0].ApprovalRef != req.ID || rollbacks[0].Unit != "web" {
		t.Fatalf("the supervisor was asked %+v", rollbacks)
	}
}

// An approval the control zone spends itself is spent once, and an approval
// of one factory is nothing to another.
func TestAnApprovalIsSpentOnceAndOnlyInItsOwnFactory(t *testing.T) {
	f := harness.New(t, harness.Options{})
	owner := f.SignInOwner()
	c := newClients(t, f, owner)
	set := call{Input: raw(`{"mode":"capped","cap_micros":5000000,"period":"month","alert_threshold_micros":4000000}`)}
	if got := c.on(t, surfaceWebMCP, "budget.set", set); got.Code != core.CodeApprovalRequired {
		t.Fatalf("a budget change without an approval answered %q: %s", got.Code, got.Message)
	}
	req := openRequest(t, c, "budget.set")
	if got := c.on(t, surfaceDashboard, "approvals.decide", call{Input: decideInput(req)}); got.Code != "" {
		t.Fatalf("decide: %q %s", got.Code, got.Message)
	}

	other := harness.New(t, harness.Options{FactoryID: "acme-other"})
	otherOwner := other.SignInOwner()
	oc := newClients(t, other, otherOwner)
	set.ApprovalID = req.ID
	if got := oc.on(t, surfaceWebMCP, "budget.set", set); got.Code == "" {
		t.Fatal("another factory accepted this factory's approval")
	}

	if got := c.on(t, surfaceWebMCP, "budget.set", set); got.Code != "" {
		t.Fatalf("the approved budget change answered %q: %s", got.Code, got.Message)
	}
	if got := c.on(t, surfaceWebMCP, "budget.set", set); got.Code != core.CodeReplayed {
		t.Fatalf("a replayed approval answered %q: %s", got.Code, got.Message)
	}
}
