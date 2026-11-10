//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the journal is read from the install's SQLite file

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// The Standalone test makes a fresh install in a temporary directory with
// the sisf binary, prepares its SQLite store, and runs the fake GitHub, the
// control plane and the dispatcher as processes of their own. It uses ports
// a Docker install leaves free, so it runs while one is up.
const (
	standaloneControl    = "127.0.0.1:7466"
	standaloneDispatcher = "127.0.0.1:7462"
	standaloneGitHub     = "127.0.0.1:7468"
)

type standaloneDeployment struct {
	dir, bin string
	m        *manifest.Manifest
	env      map[string][]string // per role: the environment it runs with
	dispatch *exec.Cmd
	logs     string
}

func newStandaloneDeployment(t *testing.T) *standaloneDeployment {
	t.Helper()
	root := t.TempDir()
	d := &standaloneDeployment{dir: filepath.Join(root, "factory"), bin: filepath.Join(root, "bin"), logs: filepath.Join(root, "logs")}
	for _, dir := range []string{d.bin, d.logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, pkg := range map[string]string{"sisf": "./cmd/sisf", "factoryd": "./cmd/factoryd"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(d.bin, name), pkg)
		cmd.Dir = moduleRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
	init := exec.Command(filepath.Join(d.bin, "sisf"), "init", "--dir", d.dir, "--mode", "standalone", "--profile", "sim",
		"--confirm-unlimited", "--no-alert")
	init.Env = []string{"HOME=" + root, "PATH=" + os.Getenv("PATH")}
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("sisf init: %v\n%s", err, out)
	}
	m, err := manifest.Load(filepath.Join(d.dir, "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d.m = m
	if err := os.MkdirAll(filepath.Join(d.dir, "data", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := install.Bootstrap(ctx, install.BootstrapConfig{FactoryID: m.FactoryID, Dialect: sqlstore.DialectSQLite, DSN: d.stateDB(),
		Budget: &install.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: time.Now()}}); err != nil {
		t.Fatalf("prepare the store: %v", err)
	}

	common := []string{"SISF_CONFIG=" + filepath.Join(d.dir, "factory.yaml"), "SISF_FACTORY_ID=" + string(m.FactoryID)}
	d.env = map[string][]string{
		"sim": d.areaEnv(t, "sim"),
		"control": append(append(common, d.areaEnv(t, "control")...), "SISF_LISTEN="+standaloneControl,
			"SISF_GITHUB_API_URL=http://"+standaloneGitHub, "SISF_DISPATCHER_HEALTH_URL=http://"+standaloneDispatcher+"/healthz",
			"SISF_APP_HEALTH_URL=http://"+standaloneDispatcher+"/healthz"),
		"agent": append(append(common, d.areaEnv(t, "agent")...), "SISF_LISTEN="+standaloneDispatcher),
	}
	d.start(t, "sim-github", "sim", filepath.Join(d.bin, "sisf"), "sim", "github", "--addr="+standaloneGitHub,
		"--repo="+d.github().repo, "--webhook-url=http://"+standaloneControl+"/webhooks/github")
	d.start(t, "control", "control", filepath.Join(d.bin, "factoryd"), "control")
	d.startDispatcher(t)
	base, _ := d.control(t)
	eventually(t, "the control plane", func() (bool, string) { return get(base+"/healthz") == nil, "" })
	return d
}

func (d *standaloneDeployment) stateDB() string {
	return filepath.Join(d.dir, "data", "state", "factory.db")
}

// areaEnv reads the env file the installer wrote for one area.
func (d *standaloneDeployment) areaEnv(t *testing.T, area string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "secrets", area, "runtime.env"))
	if err != nil {
		t.Fatalf("the %s env file cannot be read", area)
	}
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, "=") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// start runs one unit as a process of its own, with only its zone's
// environment, until the test ends.
func (d *standaloneDeployment) start(t *testing.T, name, role, path string, args ...string) *exec.Cmd {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(d.logs, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.dir, d.env[role], log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			tail, _ := os.ReadFile(log.Name())
			t.Logf("%s log:\n%s", name, lastLines(string(tail), 30))
		}
	})
	return cmd
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func get(url string) error {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return nil
}

func (d *standaloneDeployment) github() simGitHub {
	return newSimGitHub("http://"+standaloneGitHub, d.m.Repository.Owner+"/"+d.m.Repository.Repo)
}

func (d *standaloneDeployment) control(t *testing.T) (string, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "secrets", "control", "owner-token"))
	if err != nil {
		t.Fatal("the Owner token cannot be read from the install")
	}
	return "http://" + standaloneControl, strings.TrimSpace(string(raw))
}

func (d *standaloneDeployment) factoryYAML(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// factoryd runs one factoryd command with a role's environment.
func (d *standaloneDeployment) factoryd(role string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, filepath.Join(d.bin, "factoryd"), args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.dir, d.env[role], &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (d *standaloneDeployment) approvePolicy(t *testing.T, revision string) {
	t.Helper()
	if out, err := d.factoryd("control", "policy", "approve", "--revision", revision); err != nil {
		t.Fatalf("policy approve: %v\n%s", err, out)
	}
}

func (d *standaloneDeployment) dispatcherHealth(*testing.T) (health, error) {
	var h health
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + standaloneDispatcher + "/healthz")
	if err != nil {
		return h, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("dispatcher health answered %d: %v", resp.StatusCode, err)
	}
	return h, json.Unmarshal(raw, &h)
}

func (d *standaloneDeployment) killDispatcher(t *testing.T) {
	t.Helper()
	if err := d.dispatch.Process.Kill(); err != nil {
		t.Fatalf("kill the dispatcher: %v", err)
	}
	_ = d.dispatch.Wait() // killed: the exit status says so and nothing more
}

func (d *standaloneDeployment) startDispatcher(t *testing.T) {
	t.Helper()
	d.dispatch = d.start(t, "dispatcher", "agent", filepath.Join(d.bin, "factoryd"), "dispatcher")
}

func (d *standaloneDeployment) report(_ *testing.T, issue, run, owner string, token int64) (string, error) {
	return d.factoryd("agent", "report", "--issue", issue, "--run", run, "--owner", owner, "--token", fmt.Sprint(token),
		"--pr", d.github().repo+"#999")
}

func (d *standaloneDeployment) journey(t *testing.T, issue string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.stateDB()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT to_state FROM transitions WHERE factory_id = ? AND issue = ? ORDER BY version`, string(d.m.FactoryID), issue)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStandaloneFactoryTakesAnIssueToARunAndSurvivesADispatcherKill(t *testing.T) {
	runLifecycle(t, newStandaloneDeployment(t))
}
