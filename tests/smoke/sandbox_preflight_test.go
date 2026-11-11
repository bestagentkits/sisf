//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// factoryLabelPrefix starts every label the factory reads or writes.
const factoryLabelPrefix = "sisf:"

// requireIdleSandbox refuses a paid run while the sandbox holds open work
// a fresh install would take up: its first reconciliation reads every issue
// of the repository and intake takes any open one labelled sisf:ready, so
// a left-over issue becomes a second run and a second spend. An open pull
// request of an earlier run would compete with this run's. It reads GitHub
// and changes nothing.
func requireIdleSandbox(ctx context.Context, r *commandRunner) {
	r.t.Helper()
	issues, err := r.run(ctx, ghLimit, nil, "gh", "issue", "list", "--repo", sandboxRepo, "--state", "open",
		"--json", "number,labels", "--limit", "1000")
	if err != nil {
		r.fatalf("preflight: the open issues of %s cannot be listed: %v", sandboxRepo, err)
	}
	pulls, err := r.run(ctx, ghLimit, nil, "gh", "pr", "list", "--repo", sandboxRepo, "--state", "open",
		"--json", "number", "--limit", "1000")
	if err != nil {
		r.fatalf("preflight: the open pull requests of %s cannot be listed: %v", sandboxRepo, err)
	}
	if busy := openFactoryWork(issues, pulls); busy != "" {
		r.fatalf("preflight: %s has open factory work a fresh install would take up (%s); close it before a paid run", sandboxRepo, busy)
	}
	r.report.check("sandbox has no open factory work", statusPass, "no open issue with a "+factoryLabelPrefix+" label and no open pull request")
	reportLeftoverCandidateBranches(ctx, r)
}

// candidateBranchPrefix starts every branch the supervisor commits a
// candidate to: sisf/<factory>/<run>-<attempt>.
const candidateBranchPrefix = "sisf/"

// reportLeftoverCandidateBranches notes the candidate branches earlier runs
// left in the sandbox. A new run commits to branches of its own run id, so
// they do not stop it, but they accumulate: the Owner deletes the ones
// whose pull request is merged or abandoned. The run reads them and
// deletes nothing.
func reportLeftoverCandidateBranches(ctx context.Context, r *commandRunner) {
	r.t.Helper()
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "--paginate",
		"repos/"+sandboxRepo+"/git/matching-refs/heads/"+candidateBranchPrefix, "--jq", ".[].ref")
	if err != nil {
		r.report.check("leftover candidate branches", statusInfo, "the sandbox's branches cannot be listed: "+err.Error())
		return
	}
	r.report.check("leftover candidate branches", statusInfo, leftoverCandidateBranches(out))
}

// leftoverCandidateBranches describes, from the refs `gh api` lists one per
// line, the candidate branches of earlier runs.
func leftoverCandidateBranches(refs string) string {
	var names []string
	for ref := range strings.FieldsSeq(refs) {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok && strings.HasPrefix(name, candidateBranchPrefix) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d from earlier runs, left in place (delete them by hand once their pull requests are merged or closed): %s",
		len(names), strings.Join(names, ", "))
}

// openFactoryWork lists, from the JSON of `gh issue list` and `gh pr list`,
// the open issues carrying a sisf: label and the open pull requests; ""
// when there are none.
func openFactoryWork(issuesJSON, pullsJSON string) string {
	var issues []struct {
		Number int
		Labels []struct{ Name string }
	}
	var pulls []struct{ Number int }
	if err := json.Unmarshal([]byte(issuesJSON), &issues); err != nil {
		return "the open issues cannot be read: " + err.Error()
	}
	if err := json.Unmarshal([]byte(pullsJSON), &pulls); err != nil {
		return "the open pull requests cannot be read: " + err.Error()
	}
	var found []string
	for _, issue := range issues {
		var labels []string
		for _, l := range issue.Labels {
			if strings.HasPrefix(l.Name, factoryLabelPrefix) {
				labels = append(labels, l.Name)
			}
		}
		if len(labels) > 0 {
			found = append(found, fmt.Sprintf("issue #%d (%s)", issue.Number, strings.Join(labels, ", ")))
		}
	}
	for _, pr := range pulls {
		found = append(found, fmt.Sprintf("pull request #%d", pr.Number))
	}
	return strings.Join(found, "; ")
}
