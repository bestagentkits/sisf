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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the journal is read from the install's SQLite file

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The Standalone test drives the sisf command line the way an Owner does:
// `sisf init` and `sisf up --mode standalone --profile sim` in a fresh
// SISF_HOME, `sisf doctor`, the lifecycle scenario, and `sisf down`. Every
// unit of the three zones runs as a local process supervised by `sisf up`,
// on SQLite, on the fixed loopback ports of an install; the test fails at
// once when another factory holds them.
var (
	standaloneEdge       = loopbackAddr(install.PortEdge)
	standaloneControl    = loopbackAddr(install.PortControl)
	standaloneSupervisor = loopbackAddr(install.PortSupervisor)
	standaloneDispatcher = loopbackAddr(install.PortDispatcher)
	standaloneGitHub     = loopbackAddr(install.PortFakeGitHub)
)

func loopbackAddr(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

type standaloneDeployment struct {
	ownerSignIn
	t       *testing.T
	root    string // the test's temporary directory, holding everything below
	home    string // SISF_HOME: the install directory
	sisfBin string // the sisf command line under test
	upLog   string
	m       *manifest.Manifest
	env     map[string][]string // per zone area: the environment its factoryd commands run with

	up       *exec.Cmd
	upExited chan struct{}
	killed   int // pid of the dispatcher killed last
}

func newStandaloneDeployment(t *testing.T) *standaloneDeployment {
	t.Helper()
	requireFreeFactoryPorts(t)
	// The real path, so the command lines of the units match it on macOS,
	// where the temporary directory sits behind a symbolic link.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &standaloneDeployment{
		t: t, root: root, home: filepath.Join(root, "factory"), sisfBin: filepath.Join(root, "sisf"), upLog: filepath.Join(root, "up.log"),
	}
	build := exec.Command("go", "build", "-o", d.sisfBin, "./cmd/sisf")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sisf: %v\n%s", err, out)
	}
	if out, err := d.sisf(time.Minute, "init", "--mode", "standalone", "--profile", "sim", "--confirm-unlimited", "--no-alert"); err != nil {
		t.Fatalf("sisf init: %v\n%s", err, out)
	}

	d.startUp(t)
	d.waitUntilUp(t)
	out, err := d.sisf(2*time.Minute, "doctor")
	if err != nil || !strings.Contains(out, " 0 failures") {
		t.Fatalf("sisf doctor on the running factory: %v\n%s", err, out)
	}

	// `sisf up` pinned the built programs in the manifest; read it as it is now.
	m, err := manifest.Load(filepath.Join(d.home, "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d.m = m
	ext, _ := m.Extensions["github_api_url"].(string)
	common := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"SISF_CONFIG=" + filepath.Join(d.home, "factory.yaml"), "SISF_FACTORY_ID=" + string(m.FactoryID)}
	d.env = map[string][]string{
		"control": append(append(common, d.areaEnv(t, "control")...), "SISF_GITHUB_API_URL="+ext),
		"agent":   append(common, d.areaEnv(t, "agent")...),
	}
	return d
}

func (d *standaloneDeployment) stateDB() string {
	return filepath.Join(d.home, "data", "state", "factory.db")
}

// areaEnv reads the env file the installer wrote for one area.
func (d *standaloneDeployment) areaEnv(t *testing.T, area string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.home, "secrets", area, "runtime.env"))
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

func (d *standaloneDeployment) factoryYAML(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.home, "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// factoryd runs one command of the install's own factoryd with a zone's
// environment, as the Owner would on the factory's host.
func (d *standaloneDeployment) factoryd(area string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, filepath.Join(d.home, "bin", "factoryd"), args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.home, d.env[area], &out, &out
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

// killDispatcher kills the dispatcher unit outright, the way a crash would,
// and waits until it is gone.
func (d *standaloneDeployment) killDispatcher(t *testing.T) {
	t.Helper()
	pid := d.dispatcherPID(t)
	if pid == 0 {
		t.Fatal("sisf up runs no dispatcher")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the dispatcher: %v", err)
	}
	d.killed = pid
	eventually(t, "the killed dispatcher to be gone", func() (bool, string) {
		return !alive(pid), "process " + strconv.Itoa(pid) + " is still there"
	})
}

// startDispatcher waits for `sisf up` to start the dispatcher again: in
// Standalone mode it is the supervising process that restarts a dead unit.
func (d *standaloneDeployment) startDispatcher(t *testing.T) {
	t.Helper()
	eventually(t, "sisf up to restart the dispatcher", func() (bool, string) {
		pid := d.dispatcherPID(t)
		return pid != 0 && pid != d.killed, "dispatcher process " + describePID(pid)
	})
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
	d := newStandaloneDeployment(t)
	runLifecycle(t, d)
	// The same factory serves every surface to a person who signed in; it
	// shares the bring-up to keep the suite's wall time down.
	t.Run("surfaces through sign-in", func(t *testing.T) { runSurfaces(t, d) })
	// `sisf down` stops every unit `sisf up` started and `sisf up` itself;
	// the cleanup then checks that the ports are free.
	d.down(t)
	if left := d.installProcesses(t); len(left) > 0 {
		t.Fatalf("processes of this install outlived sisf down: %v", left)
	}
}
