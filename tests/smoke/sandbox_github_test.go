//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The run's own GitHub calls, all through the gh login and all aimed at
// the sandbox, except one read of the account's public activity that
// proves nothing else was touched.

const ghLimit = time.Minute

// sandboxURL is the web prefix of everything the run may touch.
const sandboxURL = "https://github.com/" + sandboxRepo + "/"

// openIssue opens the run's one issue and returns its number and URL.
func openIssue(ctx context.Context, r *commandRunner) (int, string, error) {
	args := []string{"issue", "create", "--repo", sandboxRepo, "--title", issueTitle, "--body", issueBody}
	for _, l := range issueLabels {
		args = append(args, "--label", l)
	}
	out, err := r.run(ctx, ghLimit, nil, "gh", args...)
	if err != nil {
		return 0, "", err
	}
	url := lastLines(strings.TrimSpace(out), 1)
	number, err := issueNumber(url)
	return number, url, err
}

// issueNumber reads the number from a sandbox issue URL and refuses any
// other repository's URL.
func issueNumber(url string) (int, error) {
	rest, ok := strings.CutPrefix(url, sandboxURL+"issues/")
	n, err := strconv.Atoi(rest)
	if !ok || err != nil || n <= 0 {
		return 0, fmt.Errorf("gh issue create answered %q, not a sandbox issue URL", url)
	}
	return n, nil
}

// issueState is OPEN or CLOSED, with the reason GitHub records.
func issueState(ctx context.Context, r *commandRunner, number int) (string, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "issue", "view", strconv.Itoa(number), "--repo", sandboxRepo,
		"--json", "state,stateReason", "--jq", `.state + " " + .stateReason`)
	return strings.TrimSpace(out), err
}

// progressComments counts the factory's progress comments on the issue.
func progressComments(ctx context.Context, r *commandRunner, number int) (int, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", sandboxRepo, number),
		"--jq", `[.[] | select(.body | contains("<!-- sisf:progress "))] | length`)
	if err != nil {
		return 0, err
	}
	total := 0
	for line := range strings.FieldsSeq(out) { // one count per page
		n, err := strconv.Atoi(line)
		if err != nil {
			return 0, fmt.Errorf("gh api answered %q, not a count", line)
		}
		total += n
	}
	return total, nil
}

// pullRequestLinks reports whether the pull request belongs to the sandbox
// and refers to the issue, and returns its state for the report.
func pullRequestLinks(ctx context.Context, r *commandRunner, url string, number int) (bool, string, error) {
	if !strings.HasPrefix(url, sandboxURL+"pull/") {
		return false, "", fmt.Errorf("the pull request %q is not in the sandbox", url)
	}
	out, err := r.run(ctx, ghLimit, nil, "gh", "pr", "view", url, "--json", "state,body,closingIssuesReferences")
	if err != nil {
		return false, "", err
	}
	var pr struct {
		State                   string
		Body                    string
		ClosingIssuesReferences []struct{ Number int }
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return false, "", fmt.Errorf("gh pr view answered something that is not JSON: %w", err)
	}
	for _, ref := range pr.ClosingIssuesReferences {
		if ref.Number == number {
			return true, pr.State, nil
		}
	}
	return strings.Contains(pr.Body, "#"+strconv.Itoa(number)), pr.State, nil
}

// activityOutsideSandbox lists the account's public events since start
// that name a repository other than the sandbox. GitHub publishes events
// with a delay of up to a few minutes, and the check assumes nobody else
// works with the same account during the run.
func activityOutsideSandbox(ctx context.Context, r *commandRunner, login string, start time.Time) ([]string, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "users/"+login+"/events?per_page=100",
		"--jq", `.[] | [.created_at, .repo.name, .type] | @tsv`)
	if err != nil {
		return nil, err
	}
	var outside []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		at, err := time.Parse(time.RFC3339, fields[0])
		if err != nil || at.Before(start.Add(-time.Minute)) {
			continue
		}
		if fields[1] != sandboxRepo {
			outside = append(outside, strings.Join(fields, " "))
		}
	}
	return outside, nil
}

// defaultBranchRevision is the commit the policy is approved at.
func defaultBranchRevision(ctx context.Context, r *commandRunner) (string, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo+"/commits/HEAD", "--jq", ".sha")
	sha := strings.TrimSpace(out)
	if err == nil && len(sha) != 40 {
		err = fmt.Errorf("the sandbox's head commit %q is not a commit id", sha)
	}
	return sha, err
}
