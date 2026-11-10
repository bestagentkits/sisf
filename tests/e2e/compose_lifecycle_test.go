//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The Compose test runs against the Docker install that `sisf up --mode
// docker --profile sim` started in $SISF_HOME, with the budget confirmed. It
// changes nothing but that install's own dispatcher container, which it
// kills and starts again.

// composeDeployment is a running Docker install.
type composeDeployment struct {
	ownerSignIn
	dir     string
	project string
	repo    string
}

func newComposeDeployment(t *testing.T) *composeDeployment {
	t.Helper()
	dir := os.Getenv("SISF_HOME")
	if dir == "" {
		t.Skip("SISF_HOME is not set: start a factory with `sisf up --mode docker --profile sim` and point SISF_HOME at it")
	}
	m, docker, err := dockerInstall(dir)
	if err != nil {
		t.Fatalf("the install in SISF_HOME cannot be read: %v", err)
	}
	if !docker || m.Extensions["profile"] != "sim" {
		t.Fatalf("the install in SISF_HOME is not a Docker install of the simulation profile")
	}
	d := &composeDeployment{dir: dir, project: string(m.FactoryID), repo: m.Repository.Owner + "/" + m.Repository.Repo}
	for _, unit := range []string{"control", "dispatcher", "sim-github", "state"} {
		out, err := d.docker(t, "inspect", "--format", "{{.State.Running}}", d.container(unit))
		if err != nil || strings.TrimSpace(out) != "true" {
			t.Fatalf("%s is not running: %v %s", d.container(unit), err, out)
		}
	}
	return d
}

// dockerInstall reads the install in dir and reports whether it runs in
// Docker mode.
func dockerInstall(dir string) (*manifest.Manifest, bool, error) {
	m, err := manifest.Load(filepath.Join(dir, "factory.yaml"))
	if err != nil {
		return nil, false, err
	}
	return m, m.Mode == manifest.ModeDocker, nil
}

func (d *composeDeployment) container(unit string) string { return d.project + "-" + unit }

// docker runs the docker CLI and returns its combined output.
func (d *composeDeployment) docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (d *composeDeployment) github() simGitHub {
	return newSimGitHub("http://127.0.0.1:7467", d.repo)
}

func (d *composeDeployment) factoryYAML(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (d *composeDeployment) approvePolicy(t *testing.T, revision string) {
	t.Helper()
	out, err := d.docker(t, "exec", d.container("control"), "factoryd", "policy", "approve", "--revision", revision)
	switch {
	case err != nil && strings.Contains(out, "APPROVAL_REQUIRED"):
		// `sisf up` pins the images it built in factory.yaml, so a rebuild is
		// a policy change, which this test does not approve.
		t.Fatalf("another policy is in force in this install's state, approved before its images were rebuilt; "+
			"start from fresh state with `sisf down -v` and `sisf up`\n%s", out)
	case err != nil:
		t.Fatalf("policy approve: %v\n%s", err, out)
	}
}

func (d *composeDeployment) dispatcherHealth(t *testing.T) (health, error) {
	t.Helper()
	var h health
	out, err := d.docker(t, "exec", d.container("dispatcher"), "wget", "-q", "-O", "-", "http://127.0.0.1:7465/healthz")
	if err != nil {
		return h, fmt.Errorf("%v: %s", err, out)
	}
	return h, json.Unmarshal([]byte(out), &h)
}

func (d *composeDeployment) killDispatcher(t *testing.T) {
	t.Helper()
	if out, err := d.docker(t, "kill", d.container("dispatcher")); err != nil {
		t.Fatalf("kill the dispatcher: %v\n%s", err, out)
	}
}

func (d *composeDeployment) startDispatcher(t *testing.T) {
	t.Helper()
	if out, err := d.docker(t, "start", d.container("dispatcher")); err != nil {
		t.Fatalf("start the dispatcher: %v\n%s", err, out)
	}
}

func (d *composeDeployment) report(t *testing.T, issue, run, owner string, token int64) (string, error) {
	t.Helper()
	return d.docker(t, "exec", d.container("dispatcher"), "factoryd", "report", "--issue", issue, "--run", run,
		"--owner", owner, "--token", fmt.Sprint(token), "--pr", d.repo+"#999")
}

// issuePattern is the only shape of issue reference put into a query.
var issuePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+$`)

func (d *composeDeployment) journey(t *testing.T, issue string) []string {
	t.Helper()
	if !issuePattern.MatchString(issue) {
		t.Fatalf("not an issue reference: %q", issue)
	}
	query := fmt.Sprintf("SELECT to_state FROM transitions WHERE factory_id = '%s' AND issue = '%s' ORDER BY version", d.project, issue)
	out, err := d.docker(t, "exec", d.container("state"), "psql", "-U", "sisf_owner", "-d", "sisf", "-At", "-c", query)
	if err != nil {
		t.Fatalf("read the journal: %v\n%s", err, out)
	}
	return strings.Fields(out)
}

func TestComposeFactoryTakesAnIssueToARunAndSurvivesADispatcherKill(t *testing.T) {
	runLifecycle(t, newComposeDeployment(t))
}

// The Docker factory serves every surface to a person who signed in at the
// fake identity provider the simulation runs beside control.
func TestComposeServesEverySurfaceToASignedInPerson(t *testing.T) {
	runSurfaces(t, newComposeDeployment(t))
}
