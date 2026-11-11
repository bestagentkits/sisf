package runactions_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// releases are the held or refused releases the supervisor recorded, by run.
type releases map[core.RunID]runactions.ReleaseHold

func (r releases) ReleaseHold(_ context.Context, run core.RunID, _ int) (runactions.ReleaseHold, error) {
	hold, ok := r[run]
	if !ok {
		return runactions.ReleaseHold{}, core.NewError(core.CodeNotFound, "no release hold")
	}
	return hold, nil
}

// The supervisor's reason for a held release is its own error text, meant
// for the Owner. runs.inspect and runs.explain give it to the Owner only;
// every other role reading them sees the public summary and the code.
func TestTheSupervisorsReleaseReasonIsShownToTheOwnerOnly(t *testing.T) {
	t.Parallel()
	const reason = "stage build: open /srv/factory/internal-path: permission denied"
	w := world{
		records: records{issueRef(1): record(1, core.StateReleaseCandidate, "run-1", 1)},
		runs:    runs{"run-1": {Issue: issueRef(1), RunID: "run-1", Status: dispatcher.RunSucceeded, Attempt: 1}},
		releases: releases{"run-1": {Result: "held", Code: string(core.CodeReleaseHold),
			Summary: "The release is held until it can be judged.", Reason: reason, Ref: "release-hold/run-1/1", At: t0}},
	}
	inspect, explain := w.action(t, runactions.ActionRunsInspect), w.action(t, runactions.ActionRunsExplain)
	in := `{"issue":"acme/app#1"}`
	for _, c := range []struct {
		role  core.Role
		shown bool
	}{
		{core.RoleOwner, true},
		{core.RoleMaintainer, false},
		{core.RoleReporter, false},
		{core.RoleViewer, false},
		{core.RoleAgent, false},
	} {
		actor := core.Actor{ID: "user:" + string(c.role), Kind: core.ActorHuman, Roles: []core.Role{c.role}}
		seen, err := callAs[runactions.Inspection](t, inspect, in, actor)
		if err != nil {
			t.Fatal(err)
		}
		if seen.ReleaseHold == nil || seen.ReleaseHold.Code != string(core.CodeReleaseHold) || seen.ReleaseHold.Summary == "" {
			t.Fatalf("%s: runs.inspect shows the hold %+v, want its code and summary", c.role, seen.ReleaseHold)
		}
		if (seen.ReleaseHold.Reason == reason) != c.shown {
			t.Errorf("%s: runs.inspect gives the reason %q, shown to this role: %v", c.role, seen.ReleaseHold.Reason, c.shown)
		}
		why, err := callAs[runactions.Explanation](t, explain, in, actor)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(why)
		if strings.Contains(string(raw), "permission denied") != c.shown {
			t.Errorf("%s: runs.explain answers %s, reason shown to this role: %v", c.role, raw, c.shown)
		}
		if !strings.Contains(string(raw), string(core.CodeReleaseHold)) {
			t.Errorf("%s: runs.explain does not give the code: %s", c.role, raw)
		}
	}
}
