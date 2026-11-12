//go:build e2e

package e2e

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// seedPolicy makes the seed files (foundation) the policy every candidate
// of the scenario is built on, and main's head.
//
// The first time, on a fresh install, they are committed alone and put in
// force without an approval, as the first policy revision is. While main
// has not moved since, nothing changes. Once a release was merged, main has
// moved past the policy in force, and a later release of the same unit is
// refused unless the policy contains the merged one. The Owner then moves
// the baseline as the operations guide says: a commit on main's head that
// holds the seed files again, so it contains every merged release and the
// scripted diffs still apply, filed as a policy change, decided on the
// dashboard after a step-up and put in force with that approval.
func seedPolicy(t *testing.T, d deployment) {
	t.Helper()
	gh := d.github()
	files := foundation(t, d.factoryYAML(t))
	inForce := policyInForce(t, d)
	switch {
	case inForce == "":
		d.approvePolicy(t, gh.commit(t, files))
	case inForce == gh.mainHead(t):
	default:
		ownerMovesBaseline(t, d, gh.commitOnMain(t, files, "Restore the seed files on top of the merged releases"))
	}
}

// policyInForce is the revision of the policy in force, "" before the first.
func policyInForce(t *testing.T, d deployment) string {
	t.Helper()
	raw := d.kv(t, "policy", "baseline")
	if raw == "" {
		return ""
	}
	var baseline struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil || baseline.Revision == "" {
		t.Fatalf("the policy in force cannot be read: %v: %s", err, raw)
	}
	return baseline.Revision
}

// ownerMovesBaseline puts revision in force in place of the policy in force,
// the way the Owner changes the policy: request, decision on the dashboard,
// then approval with that decision.
func ownerMovesBaseline(t *testing.T, d deployment, revision string) {
	t.Helper()
	out, err := d.policy(t, "request", "--revision", revision)
	if err != nil {
		t.Fatalf("policy request: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if i := strings.LastIndexByte(id, '\n'); i >= 0 {
		id = strings.TrimSpace(id[i+1:])
	}

	owner := harness.NewBrowser(t, controlURL, fakeIdentityProvider)
	owner.SignIn(wiring.ProviderOIDC, ownerSubject)
	request := pendingApproval(t, owner.PAT("approvals:read"), "policy.approve_revision")
	if request.ID != id {
		t.Fatalf("the newest pending policy change is %s, want %s", request.ID, id)
	}
	decide := url.Values{"request_id": {request.ID}, "nonce": {request.Nonce}, "subject_digest": {request.SubjectDigest},
		"policy_revision": {request.PolicyRevision}, "decision": {"approve"}}
	if code := confirmedForm(t, owner, "approvals.decide", decide); code != "" {
		t.Fatalf("the Owner could not approve the policy change on the dashboard: %q", code)
	}

	if out, err := d.policy(t, "approve", "--revision", revision, "--approval", id); err != nil {
		t.Fatalf("policy approve with the Owner's approval: %v\n%s", err, out)
	}
	if got := policyInForce(t, d); got != revision {
		t.Fatalf("the policy in force is %s after the Owner moved it to %s", got, revision)
	}
}
