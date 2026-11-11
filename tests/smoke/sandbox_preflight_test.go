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
