//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Helpers that run the sisf command line against a Standalone install and
// keep track of the foreground `sisf up` and the units it supervises.

// The loopback ports a Factory install binds, in either mode.
const firstFactoryPort, lastFactoryPort = 7460, 7469

const (
	// upLimit bounds how long `sisf up` may take to build the programs and
	// bring every unit up; a cold build cache on a loaded machine is slow.
	upLimit = 10 * time.Minute
	// downLimit bounds `sisf down`, which waits out every unit's grace period.
	downLimit = 2 * time.Minute
)

// requireFreeFactoryPorts skips the test when SISF_HOME points at a Docker
// install: that install holds the ports while the Compose tests run against
// it, and the Standalone test runs in a `make e2e` without SISF_HOME. Other
// than that it fails the test at once when another factory, of either mode,
// holds one of the ports a Standalone install needs.
func requireFreeFactoryPorts(t *testing.T) {
	t.Helper()
	if home := os.Getenv("SISF_HOME"); home != "" {
		if _, docker, err := dockerInstall(home); err == nil && docker {
			t.Skip("SISF_HOME points at a Docker install, whose factory holds the ports; " +
				"the Standalone test runs when no Docker factory is up (`make e2e` without SISF_HOME)")
		}
	}
	if busy := busyFactoryPorts(); len(busy) > 0 {
		t.Fatalf("ports %v on 127.0.0.1 are in use; the Standalone test needs %d-%d free. "+
			"Another factory (a Docker install, perhaps) is up: stop it with `sisf down` and run the test again",
			busy, firstFactoryPort, lastFactoryPort)
	}
}

// busyFactoryPorts lists the factory ports something answers on or holds.
func busyFactoryPorts() []int {
	var busy []int
	for port := firstFactoryPort; port <= lastFactoryPort; port++ {
		address := "127.0.0.1:" + strconv.Itoa(port)
		if conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			busy = append(busy, port)
			continue
		}
		ln, err := net.Listen("tcp", address)
		if err != nil {
			busy = append(busy, port)
			continue
		}
		_ = ln.Close()
	}
	return busy
}

// sisfEnv is the environment of the sisf command line: the caller's, with
// SISF_HOME pointing at the install and no other SISF_ setting leaking in.
func sisfEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SISF_") {
			env = append(env, kv)
		}
	}
	return append(env, "SISF_HOME="+home)
}

// sisf runs one sisf command to completion and returns its combined output.
func (d *standaloneDeployment) sisf(limit time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, d.sisfBin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = moduleRoot(d.t), sisfEnv(d.home), &out, &out
	err := cmd.Run()
	return out.String(), err
}

// startUp runs `sisf up --mode standalone --profile sim` in the background
// and arranges for `sisf down` to stop it, and every unit, when the test ends.
func (d *standaloneDeployment) startUp(t *testing.T) {
	t.Helper()
	log, err := os.Create(d.upLog)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(d.sisfBin, "up", "--mode", "standalone", "--profile", "sim", "--source", moduleRoot(t))
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = moduleRoot(t), sisfEnv(d.home), log, log
	// A process group of its own: an interrupt of the test run reaches the
	// test, which stops the factory through `sisf down`, not every unit at once.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start sisf up: %v", err)
	}
	d.up, d.upExited = cmd, make(chan struct{})
	go func() {
		_ = cmd.Wait() // the exit is observed through upExited
		_ = log.Close()
		close(d.upExited)
	}()
	t.Cleanup(func() { d.shutdown(t) })
}

// upRunning reports whether the foreground `sisf up` is still running.
func (d *standaloneDeployment) upRunning() bool {
	select {
	case <-d.upExited:
		return false
	default:
		return true
	}
}

// waitUntilUp polls until every unit answers, failing at once if `sisf up`
// gives up first.
func (d *standaloneDeployment) waitUntilUp(t *testing.T) {
	t.Helper()
	// The supervisor's admin socket and the fake GitHub have no health
	// route; that they accept connections is enough here, and the doctor
	// checks what they answer.
	checks := []func() error{
		func() error { return get("http://" + standaloneControl + "/healthz") },
		func() error { return get("http://" + standaloneDispatcher + "/healthz") },
		func() error { return get("http://" + standaloneEdge + "/healthz") },
		func() error { return dial(standaloneSupervisor) },
		func() error { return dial(standaloneGitHub) },
	}
	deadline := time.Now().Add(upLimit)
	last := ""
	for time.Now().Before(deadline) {
		if !d.upRunning() {
			t.Fatalf("sisf up exited before the factory was up:\n%s", d.upOutput())
		}
		last = ""
		for _, check := range checks {
			if err := check(); err != nil {
				last = err.Error()
				break
			}
		}
		if last == "" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the factory was not up within %s; last seen: %s\n%s", upLimit, last, d.upOutput())
}

// dial checks that something accepts connections on address.
func dial(address string) error {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (d *standaloneDeployment) upOutput() string {
	raw, _ := os.ReadFile(d.upLog)
	return lastLines(string(raw), 40)
}

// shutdown runs `sisf down`, waits for the foreground process to end, and
// checks that no unit is left listening. If the command fails, it stops the
// units it started itself so that nothing outlives the test.
func (d *standaloneDeployment) shutdown(t *testing.T) {
	t.Helper()
	if t.Failed() {
		d.dumpLogs(t)
	}
	if d.upRunning() {
		d.down(t)
	}
	if left := d.installProcesses(t); len(left) > 0 {
		t.Errorf("processes of this install outlived sisf up: %v; killing them", left)
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(busyFactoryPorts()) > 0 && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if busy := busyFactoryPorts(); len(busy) > 0 {
		t.Errorf("ports %v are still in use after sisf down", busy)
	}
}

// down runs `sisf down` and waits for the foreground `sisf up` to exit. If
// it does not, the units it started are stopped directly.
func (d *standaloneDeployment) down(t *testing.T) {
	t.Helper()
	out, err := d.sisf(downLimit, "down")
	if err != nil || !strings.Contains(out, "is down") {
		t.Errorf("sisf down: %v\n%s", err, out)
	}
	select {
	case <-d.upExited:
	case <-time.After(downLimit):
		t.Errorf("sisf up did not exit after sisf down; stopping its units directly")
		for _, pid := range d.childPIDs(t, "") {
			_ = syscall.Kill(-pid, syscall.SIGKILL) // each unit leads a process group of its own
		}
		_ = d.up.Process.Kill()
		<-d.upExited
	}
}

// dumpLogs prints the end of the output of `sisf up` and of every unit log.
func (d *standaloneDeployment) dumpLogs(t *testing.T) {
	t.Helper()
	t.Logf("sisf up output:\n%s", d.upOutput())
	logs, _ := filepath.Glob(filepath.Join(d.home, "logs", "*.log"))
	for _, path := range logs {
		raw, _ := os.ReadFile(path)
		t.Logf("%s:\n%s", filepath.Base(path), lastLines(string(raw), 30))
	}
}

// childPIDs lists the processes `sisf up` started whose command line starts
// with prefix (all of them for an empty prefix). Only direct children of the
// foreground process are considered, so nothing else on the machine is found.
func (d *standaloneDeployment) childPIDs(t *testing.T, prefix string) []int {
	t.Helper()
	parent := d.up.Process.Pid
	var pids []int
	for _, p := range listProcesses(t) {
		if p.ppid == parent && strings.HasPrefix(p.args, prefix) {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

type process struct {
	pid, ppid int
	args      string
}

// listProcesses reads the process table with ps, which macOS and Linux share.
func listProcesses(t *testing.T) []process {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "args=").Output()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	var ps []process
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil {
			ps = append(ps, process{pid: pid, ppid: ppid, args: strings.Join(fields[2:], " ")})
		}
	}
	return ps
}

// installProcesses lists every process whose command line names a file of
// this test's temporary directory: the programs `sisf up` built and ran, and
// the sisf command line itself. Nothing else on the machine can match.
func (d *standaloneDeployment) installProcesses(t *testing.T) []int {
	t.Helper()
	var pids []int
	for _, p := range listProcesses(t) {
		if strings.Contains(p.args, d.root+string(filepath.Separator)) {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

// dispatcherPID is the running dispatcher unit, or 0 while there is none.
func (d *standaloneDeployment) dispatcherPID(t *testing.T) int {
	t.Helper()
	pids := d.childPIDs(t, filepath.Join(d.home, "bin", "factoryd")+" dispatcher ")
	switch len(pids) {
	case 0:
		return 0
	case 1:
		return pids[0]
	default:
		t.Fatalf("sisf up runs %d dispatchers: %v", len(pids), pids)
		return 0
	}
}

// alive reports whether a process exists; the supervising `sisf up` reaps
// its children, so a killed unit disappears once it has exited.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func describePID(pid int) string {
	if pid == 0 {
		return "none"
	}
	return fmt.Sprint(pid)
}
