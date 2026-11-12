//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The release half of the Compose deployment: the scripted agent's patch,
// the store's records and key-value entries read with psql inside the state
// container, and the containers the supervisor deploys, found by the labels
// its deployment backend puts on them.

// sqlLiteral is the only shape of value put into a query here.
var sqlLiteral = regexp.MustCompile(`^[A-Za-z0-9._/#:@-]*$`)

// query runs one statement as the store's owner and returns its rows, one
// per line with fields separated by "|".
func (d *composeDeployment) query(t *testing.T, statement string, args ...string) []string {
	t.Helper()
	quoted := make([]any, len(args))
	for i, a := range args {
		if !sqlLiteral.MatchString(a) {
			t.Fatalf("not a value this test queries with: %q", a)
		}
		quoted[i] = "'" + a + "'"
	}
	out, err := d.docker(t, "exec", d.container("state"), "psql", "-U", "sisf_owner", "-d", "sisf", "-At", "-c", fmt.Sprintf(statement, quoted...))
	if err != nil {
		t.Fatalf("query the store: %v\n%s", err, out)
	}
	var rows []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line != "" {
			rows = append(rows, line)
		}
	}
	return rows
}

func (d *composeDeployment) records(t *testing.T) []issueRecord {
	t.Helper()
	var out []issueRecord
	for _, row := range d.query(t, "SELECT issue, state, run_id, attempt FROM records WHERE factory_id = %s", d.project) {
		f := strings.Split(row, "|")
		if len(f) != 4 {
			t.Fatalf("a record row has %d fields: %q", len(f), row)
		}
		attempt, _ := strconv.Atoi(f[3])
		out = append(out, issueRecord{Issue: f[0], State: f[1], RunID: f[2], Attempt: attempt})
	}
	return out
}

func (d *composeDeployment) kv(t *testing.T, namespace, key string) string {
	t.Helper()
	rows := d.query(t, "SELECT convert_from(entry_value, 'UTF8') FROM kv WHERE factory_id = %s AND namespace = %s AND entry_key = %s",
		d.project, namespace, key)
	return strings.Join(rows, "\n")
}

func (d *composeDeployment) candidateDigest(t *testing.T, issue string) string {
	t.Helper()
	rows := d.query(t, "SELECT candidate_digest FROM transitions WHERE factory_id = %s AND issue = %s AND candidate_digest <> '' ORDER BY version DESC LIMIT 1",
		d.project, issue)
	if len(rows) != 1 {
		t.Fatalf("%s has no candidate", issue)
	}
	return rows[0]
}

func (d *composeDeployment) edgeURL() string { return "http://127.0.0.1:7460" }

// setAgentPatch rewrites the patch the dispatcher reads at its start, and
// restarts the dispatcher, the scripted agent's process, with it.
func (d *composeDeployment) setAgentPatch(t *testing.T, diff string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.dir, "sim", "agent", "patch.diff"), []byte(diff), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := d.docker(t, "restart", d.container("dispatcher")); err != nil {
		t.Fatalf("restart the dispatcher: %v\n%s", err, out)
	}
	eventually(t, "the restarted dispatcher's health", func() (bool, string) {
		_, err := d.dispatcherHealth(t)
		return err == nil, fmt.Sprint(err)
	})
}

// deployed lists the containers the supervisor's deployment backend runs
// for unit, as name and the digest it is labelled with.
func (d *composeDeployment) deployed(t *testing.T, unit string, all bool) map[string]string {
	t.Helper()
	args := []string{"ps", "--filter", "label=sisf.factory=" + d.project, "--filter", "label=sisf.unit=" + unit,
		"--format", `{{.Names}} {{.Label "sisf.digest"}}`}
	if all {
		args = append(args, "-a")
	}
	out, err := d.docker(t, args...)
	if err != nil {
		t.Fatalf("list the deployed containers: %v\n%s", err, out)
	}
	found := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if name, digest, ok := strings.Cut(line, " "); ok {
			found[name] = digest
		}
	}
	return found
}

// newComposeRelease is the running Docker install, ready for a release
// scenario.
func newComposeRelease(t *testing.T) *composeDeployment {
	t.Helper()
	d := newComposeDeployment(t)
	for _, unit := range []string{"supervisor", "edge", "docker-filter-deploy", "docker-filter-runner"} {
		out, err := d.docker(t, "inspect", "--format", "{{.State.Running}}", d.container(unit))
		if err != nil || strings.TrimSpace(out) != "true" {
			t.Fatalf("%s is not running: %v %s", d.container(unit), err, out)
		}
	}
	return d
}
