//go:build smoke

package smoke

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

// assertSandboxScope checks that the factory addressed only the sandbox,
// from its own records: the repository its configuration names, the issues
// of its journal and the pull requests of its run. The gh account's public
// activity is reported only as a count: the same person may work elsewhere
// during the run, which the factory did not cause, and the names of those
// repositories are nobody's business in a public report.
func assertSandboxScope(ctx context.Context, r *commandRunner, o *factoryObserver, login string, start time.Time) {
	r.t.Helper()
	m, err := install.Layout{Dir: o.home}.LoadManifest()
	repo := ""
	if err == nil {
		repo = m.Repository.Owner + "/" + m.Repository.Repo
	}
	issues, journalErr := o.journalIssues(ctx)
	pass, evidence := factoryScope(repo, issues, r.report.PullRequests)
	result(r, "the factory addressed only the sandbox", err == nil && journalErr == nil && pass,
		fmt.Sprintf("%s; manifest error %v, journal error %v", evidence, err, journalErr))

	n, err := accountEventsElsewhere(ctx, r, login, start)
	evidence = fmt.Sprintf("%d public event(s) of the gh account since the start named another repository; "+
		"they are other work of the same account, not counted against the factory, and not named here", n)
	if err != nil {
		evidence = fmt.Sprintf("the gh account's public events could not be read: %v", err)
	}
	r.report.check("other activity of the gh account", statusInfo, r.scrub.clean(evidence))
}

// factoryScope judges the factory's configured repository, journal issues
// ("owner/repo#n") and pull request URLs against the sandbox. The evidence
// holds counts only, never another repository's name.
func factoryScope(repo string, issues, pullRequests []string) (bool, string) {
	foreignIssues := 0
	for _, issue := range issues {
		if !strings.HasPrefix(issue, sandboxRepo+"#") {
			foreignIssues++
		}
	}
	foreignPRs := 0
	for _, pr := range pullRequests {
		if !strings.HasPrefix(pr, sandboxURL+"pull/") {
			foreignPRs++
		}
	}
	configured := "factory.yaml names the sandbox"
	if repo != sandboxRepo {
		configured = "factory.yaml names another repository"
	}
	evidence := fmt.Sprintf("%s; %d issue(s) in its journal and %d pull request(s) of its run",
		configured, len(issues), len(pullRequests))
	if foreignIssues > 0 || foreignPRs > 0 {
		evidence += fmt.Sprintf("; outside the sandbox: %d of %d issue(s), %d of %d pull request(s)",
			foreignIssues, len(issues), foreignPRs, len(pullRequests))
	}
	return repo == sandboxRepo && foreignIssues == 0 && foreignPRs == 0, evidence
}

// accountEventsElsewhere counts the gh account's public events since start
// that name a repository other than the sandbox.
func accountEventsElsewhere(ctx context.Context, r *commandRunner, login string, start time.Time) (int, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "users/"+login+"/events?per_page=100",
		"--jq", `.[] | [.created_at, .repo.name, .type] | @tsv`)
	if err != nil {
		return 0, err
	}
	return foreignEvents(out, start), nil
}

// foreignEvents counts the lines of created_at, repository and type that
// name another repository than the sandbox, from a minute before start on.
func foreignEvents(tsv string, start time.Time) int {
	n := 0
	for line := range strings.SplitSeq(strings.TrimSpace(tsv), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		at, err := time.Parse(time.RFC3339, fields[0])
		if err == nil && !at.Before(start.Add(-time.Minute)) && fields[1] != sandboxRepo {
			n++
		}
	}
	return n
}

// journalIssues lists every issue the factory's journal holds, read-only.
func (o *factoryObserver) journalIssues(ctx context.Context) ([]string, error) {
	db, err := sql.Open("sqlite", "file:"+o.stateDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT issue FROM transitions WHERE factory_id = ?`, o.factoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var issue string
		if err := rows.Scan(&issue); err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, rows.Err()
}
