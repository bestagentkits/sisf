//go:build smoke

package smoke

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The evidence that the run went through the real lifecycle: what the
// supervisor recorded of the QA verdict and the release, read read-only
// from the install's store like the journal, and what GitHub shows of the
// merged pull request.

// passScore is the lowest QA score the run accepts, the factory's default
// pass mark.
const passScore = 85

// supervisorNamespace and the key prefixes are where the supervisor's
// stages keep their records (cmd/supervisor/stages/doc.go).
const (
	supervisorNamespace = "supervisor"
	qaKeyPrefix         = "qa/"
	releaseKeyPrefix    = "release/"
)

// qaRecord is the part of one QA verdict the run reads.
type qaRecord struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Receipt struct {
		CandidateDigest string `json:"candidate_digest"`
		Score           int    `json:"score"`
		Checks          []struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
		Signature struct {
			KeyID     string `json:"key_id"`
			Algorithm string `json:"algorithm"`
			Value     []byte `json:"value"`
		} `json:"signature"`
	} `json:"receipt"`
}

// releaseRecord is the part of one release outcome the run reads.
type releaseRecord struct {
	Result  string `json:"result"`
	State   string `json:"state"`
	Release string `json:"release"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
}

// describeReleaseRecord says how one release ended and why: its result,
// the state it left the issue in, the release, and the supervisor's code
// and reason, so a release that did not close reads without the install.
func describeReleaseRecord(key string, r releaseRecord) string {
	out := fmt.Sprintf("%s: result %s, state %s, release %s", key, r.Result, r.State, r.Release)
	if r.Code != "" {
		out += ", code " + r.Code
	}
	if r.Reason != "" {
		out += fmt.Sprintf(", reason %q", truncate(r.Reason, 300))
	}
	return out
}

// supervisorRecords returns the supervisor's records whose keys start with
// prefix, keyed by key. The prefix is compared as text, not as a LIKE
// pattern, so a run ID cannot widen it.
func (o *factoryObserver) supervisorRecords(ctx context.Context, prefix string) (map[string][]byte, error) {
	return o.records(ctx, supervisorNamespace, prefix)
}

// records returns the records of namespace whose keys start with prefix,
// read-only like supervisorRecords.
func (o *factoryObserver) records(ctx context.Context, namespace, prefix string) (map[string][]byte, error) {
	db, err := sql.Open("sqlite", "file:"+o.stateDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT entry_key, entry_value FROM kv
		WHERE factory_id = ? AND namespace = ? AND substr(entry_key, 1, length(?)) = ?`, o.factoryID, namespace, prefix, prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]byte{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// passingReceipt finds, among the QA records of one run, a passed verdict
// whose receipt is signed and scores at least passScore, and says what it
// found either way.
func passingReceipt(records map[string][]byte) (bool, string) {
	keys := sortedKeys(records)
	if len(keys) == 0 {
		return false, "the supervisor recorded no QA verdict for the run"
	}
	var seen []string
	for _, key := range keys {
		var r qaRecord
		if err := json.Unmarshal(records[key], &r); err != nil {
			seen = append(seen, key+": unreadable")
			continue
		}
		seen = append(seen, describeQARecord(key, r))
		if r.Verdict == "passed" && r.signed() && r.Receipt.Score >= passScore {
			return true, strings.Join(seen, "; ")
		}
	}
	return false, strings.Join(seen, "; ")
}

func (r qaRecord) signed() bool {
	sig := r.Receipt.Signature
	return sig.KeyID != "" && sig.Algorithm != "" && len(sig.Value) > 0
}

// describeQARecord says what one QA verdict decided and why: its score, its
// signature, the hard gates its receipt records as failed and the
// supervisor's reason, so a failure reads without the install.
func describeQARecord(key string, r qaRecord) string {
	var failed []string
	for _, c := range r.Receipt.Checks {
		if c.Kind == "hard_gate" && !c.Passed {
			failed = append(failed, c.Name)
		}
	}
	out := fmt.Sprintf("%s: verdict %s, score %d, signed %t by %q", key, r.Verdict, r.Receipt.Score, r.signed(), r.Receipt.Signature.KeyID)
	if len(failed) > 0 {
		out += ", failed hard gates " + strings.Join(failed, ", ")
	}
	if r.Reason != "" {
		out += fmt.Sprintf(", reason %q", truncate(r.Reason, 300))
	}
	return out
}

// completedRelease finds, among the release records of one run, one the
// pipeline closed: observed, verified, recorded as the last known good and
// published.
func completedRelease(records map[string][]byte) (bool, string) {
	keys := sortedKeys(records)
	if len(keys) == 0 {
		return false, "the supervisor recorded no release for the run"
	}
	var seen []string
	for _, key := range keys {
		var r releaseRecord
		if err := json.Unmarshal(records[key], &r); err != nil {
			seen = append(seen, key+": unreadable")
			continue
		}
		seen = append(seen, describeReleaseRecord(key, r))
		if r.Result == "closed" && r.State == "Closed" && r.Release != "" {
			return true, strings.Join(seen, "; ")
		}
	}
	return false, strings.Join(seen, "; ")
}

// visitedInOrder reports whether the journal passed through every state of
// want in that order, other states between them allowed.
func visitedInOrder(journal []stateVisit, want ...string) bool {
	i := 0
	for _, v := range journal {
		if i < len(want) && v.State == want[i] {
			i++
		}
	}
	return i == len(want)
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// mergedPullRequest is what GitHub shows of a merged pull request.
type mergedPullRequest struct {
	URL         string
	State       string
	MergeCommit string
}

// pullRequestMerge reads the state and the merge commit of a sandbox pull
// request.
func pullRequestMerge(ctx context.Context, r *commandRunner, url string) (mergedPullRequest, error) {
	if !strings.HasPrefix(url, sandboxURL+"pull/") {
		return mergedPullRequest{}, fmt.Errorf("the pull request %q is not in the sandbox", url)
	}
	out, err := r.run(ctx, ghLimit, nil, "gh", "pr", "view", url, "--json", "state,mergeCommit")
	if err != nil {
		return mergedPullRequest{}, err
	}
	var pr struct {
		State       string
		MergeCommit *struct{ Oid string }
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return mergedPullRequest{}, fmt.Errorf("gh pr view answered something that is not JSON: %w", err)
	}
	merged := mergedPullRequest{URL: url, State: pr.State}
	if pr.MergeCommit != nil {
		merged.MergeCommit = pr.MergeCommit.Oid
	}
	return merged, nil
}

// changeOnDefaultBranch reports whether commit is on the sandbox's default
// branch and its diff adds the route the issue asked for.
func changeOnDefaultBranch(ctx context.Context, r *commandRunner, commit, route string) (bool, string, error) {
	if len(commit) != 40 {
		return false, "", fmt.Errorf("%q is not a commit id", commit)
	}
	branch, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo, "--jq", ".default_branch")
	if err != nil {
		return false, "", err
	}
	branch = strings.TrimSpace(branch)
	// The commit is on the branch when the branch is identical to it or
	// ahead of it.
	status, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo+"/compare/"+commit+"..."+branch, "--jq", ".status")
	if err != nil {
		return false, "", err
	}
	status = strings.TrimSpace(status)
	patches, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo+"/commits/"+commit,
		"--jq", `.files[] | select(.filename | endswith(".go")) | .patch // ""`)
	if err != nil {
		return false, "", err
	}
	adds := addsRoute(patches, route)
	evidence := fmt.Sprintf("%s against %s: %s; its Go diff adds %q: %t", commit, branch, status, route, adds)
	return (status == "identical" || status == "ahead") && adds, evidence, nil
}

// addsRoute reports whether a unified diff adds a line that names route as
// a quoted path, such as a handler registration.
func addsRoute(patch, route string) bool {
	for line := range strings.SplitSeq(patch, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") &&
			(strings.Contains(line, `"`+route+`"`) || strings.Contains(line, ` `+route+`"`) || strings.Contains(line, "`"+route+"`")) {
			return true
		}
	}
	return false
}
