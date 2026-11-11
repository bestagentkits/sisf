package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/bestagentkits/sisf/internal/install"
)

// runWithOutput runs one command line and returns its status and both streams.
func runWithOutput(t *testing.T, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = commands.run(context.Background(), &Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, args)
	return status, out.String(), errOut.String()
}

// initSim creates a simulated install of the given mode and returns its directory.
func initSim(t *testing.T, mode string, extra ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "factory")
	args := append([]string{"init", "--dir", dir, "--mode", mode, "--profile", "sim", "--repo", "acme/hello-app",
		"--confirm-unlimited", "--alert-threshold", "50"}, extra...)
	if status, _, stderr := runWithOutput(t, args...); status != exitOK {
		t.Fatalf("init failed with %d: %s", status, stderr)
	}
	return dir
}

func TestInitCreatesAConformantInstallInEveryModeAndPrintsNoSecret(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"standalone", "docker"} {
		dir := filepath.Join(t.TempDir(), "factory")
		status, stdout, stderr := runWithOutput(t, "init", "--dir", dir, "--mode", mode, "--profile", "sim",
			"--repo", "acme/hello-app", "--confirm-unlimited", "--no-alert")
		if status != exitOK {
			t.Fatalf("%s: init = %d: %s", mode, status, stderr)
		}
		for _, name := range []string{"webhook-secret", "github-token"} {
			secret, err := os.ReadFile(filepath.Join(dir, "secrets", "control", name))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stdout+stderr, strings.TrimSpace(string(secret))) {
				t.Errorf("%s: init printed the %s", mode, name)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "secrets", "control", "owner-token")); err == nil {
			t.Errorf("%s: init wrote an Owner token; the Owner signs in", mode)
		}
		if !strings.Contains(stdout, "sign in at http://127.0.0.1:7463 as the Owner http://127.0.0.1:7468#owner") {
			t.Errorf("%s: init does not say where to sign in:\n%s", mode, stdout)
		}
		if !strings.Contains(stdout, "lower-assurance") {
			t.Errorf("%s: the root key warning was not shown:\n%s", mode, stdout)
		}
		if status, out, errOut := runWithOutput(t, "conformance", "check", filepath.Join(dir, "factory.yaml")); status != exitOK {
			t.Errorf("%s: the generated manifest is not conformant (%d): %s %s", mode, status, out, errOut)
		}
	}
}

func TestInitRefusesToOverwriteAnInstall(t *testing.T) {
	t.Parallel()
	dir := initSim(t, "standalone")
	status, _, stderr := runWithOutput(t, "init", "--dir", dir, "--profile", "sim", "--repo", "acme/hello-app")
	if status != exitFailure || !strings.Contains(stderr, "ALREADY_EXISTS") {
		t.Fatalf("second init = %d %q, want ALREADY_EXISTS", status, stderr)
	}
}

func TestInitWithoutConfirmationLeavesTheBudgetUnconfirmedAndSaysSo(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "factory")
	status, stdout, _ := runWithOutput(t, "init", "--dir", dir, "--profile", "sim", "--repo", "acme/hello-app")
	if status != exitOK || !strings.Contains(stdout, "not confirmed") {
		t.Fatalf("init = %d, output %q", status, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "budget-confirmation.json")); err == nil {
		t.Fatal("a confirmation was recorded without the Owner's answer")
	}
}

func TestInstallFlagMistakesAreUsageErrors(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "factory")
	for name, args := range map[string][]string{
		"real GitHub without a token":     {"init", "--dir", dir, "--repo", "acme/app"},
		"no repository":                   {"init", "--dir", dir, "--github-token-env", "TOKEN"},
		"token env with the sim profile":  {"init", "--dir", dir, "--profile", "sim", "--github-token-env", "TOKEN"},
		"bad token variable name":         {"init", "--dir", dir, "--repo", "acme/app", "--github-token-env", "A B"},
		"unset token variable":            {"init", "--dir", dir, "--repo", "acme/app", "--github-token-env", "SISF_TEST_UNSET_TOKEN"},
		"bad repository":                  {"init", "--dir", dir, "--profile", "sim", "--repo", "acme"},
		"bad profile":                     {"init", "--dir", dir, "--profile", "prod", "--repo", "acme/app"},
		"alert without confirmation":      {"init", "--dir", dir, "--profile", "sim", "--no-alert"},
		"confirmation without an alert":   {"init", "--dir", dir, "--profile", "sim", "--confirm-unlimited"},
		"both alert settings":             {"init", "--dir", dir, "--profile", "sim", "--confirm-unlimited", "--no-alert", "--alert-threshold", "5"},
		"alert amount unreadable":         {"init", "--dir", dir, "--profile", "sim", "--confirm-unlimited", "--alert-threshold", "lots"},
		"unknown flag":                    {"up", "--nope"},
		"stray argument":                  {"stop", "extra"},
		"down flag on stop":               {"stop", "-v"},
		"a client secret as a flag value": {"init", "--dir", dir, "--profile", "sim", "--oidc-client-secret", "x"},
		"dev with the simulation":         {"init", "--dir", dir, "--profile", "sim", "--dev"},
		"gh credential and a token env":   {"init", "--dir", dir, "--dev", "--repo", "acme/app", "--github-credential", "gh-cli", "--github-token-env", "T"},
		"cap and unlimited":               {"init", "--dir", dir, "--profile", "sim", "--budget-cap", "10", "--confirm-unlimited"},
		"cap and no alert":                {"init", "--dir", dir, "--profile", "sim", "--budget-cap", "10", "--no-alert"},
		"alert at the cap":                {"init", "--dir", dir, "--profile", "sim", "--budget-cap", "10", "--alert-threshold", "10"},
		"run estimate without a cap":      {"init", "--dir", dir, "--profile", "sim", "--confirm-unlimited", "--no-alert", "--run-estimate", "1"},
		"agent judge in the simulation":   {"init", "--dir", dir, "--profile", "sim", "--qa-judge", "agent"},
		"docker exec in standalone":       {"init", "--dir", dir, "--profile", "sim", "--mode", "standalone", "--exec", "docker"},
		"both client secret sources": {"init", "--dir", dir, "--repo", "acme/app", "--oidc-client-secret-file", "/x",
			"--oidc-client-secret-env", "X"},
	} {
		if status, _, _ := runWithOutput(t, args...); status != exitUsage {
			t.Errorf("%s: status %d, want %d", name, status, exitUsage)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a failed init left a directory behind")
	}
}

func TestDebugOutsideDockerModeIsRefused(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "factory")
	if status, _, _ := runWithOutput(t, "init", "--dir", dir, "--profile", "sim", "--debug"); status == exitOK {
		t.Fatal("--debug was accepted in Standalone mode")
	}
}

func TestUpKeepsTheModeOfAnExistingInstall(t *testing.T) {
	t.Parallel()
	dir := initSim(t, "standalone")
	status, _, stderr := runWithOutput(t, "up", "--dir", dir, "--mode", "docker")
	if status != exitFailure || !strings.Contains(stderr, "fixed") {
		t.Fatalf("up in the other mode = %d %q, want a refusal", status, stderr)
	}
}

func TestLifecycleCommandsOnAMissingInstallFail(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "none")
	for _, cmd := range []string{"stop", "restart", "down"} {
		if status, _, _ := runWithOutput(t, cmd, "--dir", dir); status != exitFailure {
			t.Errorf("%s on a missing install: status %d", cmd, status)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a lifecycle command created the directory")
	}
}

func TestDoctorFailsOnAMissingInstallAndPointsAtInit(t *testing.T) {
	t.Parallel()
	status, stdout, _ := runWithOutput(t, "doctor", "--dir", filepath.Join(t.TempDir(), "none"))
	if status != exitFailure || !strings.Contains(stdout, "sisf init") {
		t.Fatalf("doctor = %d %q", status, stdout)
	}
}

func TestDoctorOnAnInstallThatIsNotRunningFailsAndNamesTheStartCommand(t *testing.T) {
	t.Parallel()
	dir := initSim(t, "standalone")
	status, stdout, _ := runWithOutput(t, "doctor", "--dir", dir)
	if status != exitFailure || !strings.Contains(stdout, "sisf up") {
		t.Fatalf("doctor = %d\n%s", status, stdout)
	}
	token, _ := os.ReadFile(filepath.Join(dir, "secrets", "control", "github-token"))
	if strings.Contains(stdout, strings.TrimSpace(string(token))) {
		t.Fatal("the doctor printed the GitHub token")
	}
}

func TestStateBootstrapPreparesASQLiteStoreAndChecksItsFlags(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "factory.db")
	status, stdout, stderr := runWithOutput(t, "state", "bootstrap", "--dialect", "sqlite", "--factory-id", "sisf-hello-app",
		"--sqlite-path", db, "--confirm-unlimited", "--alert-micros", "50000000")
	if status != exitOK || !strings.Contains(stdout, "ready") {
		t.Fatalf("bootstrap = %d %q %q", status, stdout, stderr)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("no database was created")
	}
	for name, args := range map[string][]string{
		"no dialect":            {"state", "bootstrap", "--factory-id", "x"},
		"no factory id":         {"state", "bootstrap", "--dialect", "sqlite"},
		"alert without confirm": {"state", "bootstrap", "--dialect", "sqlite", "--factory-id", "x", "--no-alert"},
		"unsettled alert":       {"state", "bootstrap", "--dialect", "sqlite", "--factory-id", "x", "--confirm-unlimited"},
	} {
		if status, _, _ := runWithOutput(t, args...); status != exitUsage {
			t.Errorf("%s: status %d, want %d", name, status, exitUsage)
		}
	}
	// A PostgreSQL run without its credentials is refused before any connection.
	if status, _, stderr := runWithOutput(t, "state", "bootstrap", "--dialect", "postgres", "--factory-id", "sisf-hello-app"); status != exitFailure || !strings.Contains(stderr, "SISF_STATE_OWNER_DSN") {
		t.Errorf("postgres without credentials = %d %q", status, stderr)
	}
}

func TestEveryInstallCommandIsListedInTheHelp(t *testing.T) {
	t.Parallel()
	_, stdout, _ := runWithOutput(t, "help")
	for _, name := range []string{"init", "up", "down", "stop", "restart", "doctor", "state bootstrap"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help does not list %q", name)
		}
	}
}

func TestLifecycleCommandsSayWhatTheyDid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"stop"}, "is stopped"},
		{[]string{"restart"}, "is restarting every unit"},
		{[]string{"down"}, "is down\n"},
		{[]string{"down", "-v"}, "is down and its data is removed"},
	} {
		dir := initSim(t, "standalone")
		// A child process stands in for the foreground `sisf up`: it is
		// alive, it is ours, it holds the run lock through the file it
		// inherits, and it ends on SIGTERM, which drops the lock.
		if err := os.MkdirAll(filepath.Join(dir, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		lock, err := os.OpenFile(filepath.Join(dir, "run", "sisf.lock"), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		stand := exec.Command("sleep", "60")
		stand.ExtraFiles = []*os.File{lock}
		err = stand.Start()
		if err == nil {
			// The holder records itself in the locked file, by pid and
			// start time, as `sisf up` does.
			err = recordRunLockOwner(lock, stand.Process.Pid)
		}
		_ = lock.Close() // the child alone holds the lock now
		if err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { _ = stand.Wait(); close(exited) }()
		t.Cleanup(func() { _ = stand.Process.Kill(); <-exited })
		pid := []byte(strconv.Itoa(stand.Process.Pid) + "\n")
		if err := os.WriteFile(filepath.Join(dir, "run", "sisf.pid"), pid, 0o600); err != nil {
			t.Fatal(err)
		}
		status, stdout, stderr := runWithOutput(t, append(tc.args, "--dir", dir)...)
		if status != exitOK || !strings.Contains(stdout, "the factory in "+dir+" "+tc.want) {
			t.Errorf("%v = %d %q %q, want a line saying it %s", tc.args, status, stdout, stderr, tc.want)
		}
	}
}

// Init finds the source checkout by walking up from the current directory,
// and every later up builds the supervisor from it, so it names the one it
// chose: the default and one given with --source.
func TestInitNamesTheSourceCheckoutItRecorded(t *testing.T) {
	t.Parallel()
	given := t.TempDir()
	if err := os.WriteFile(filepath.Join(given, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", given} {
		dir := filepath.Join(t.TempDir(), "factory")
		args := []string{"init", "--dir", dir, "--profile", "sim", "--repo", "acme/hello-app", "--confirm-unlimited", "--no-alert"}
		if source != "" {
			args = append(args, "--source", source)
		}
		status, stdout, stderr := runWithOutput(t, args...)
		if status != exitOK {
			t.Fatalf("init = %d: %s", status, stderr)
		}
		recorded, err := install.RecordedSource(dir)
		if err != nil || recorded == "" {
			t.Fatalf("recorded source = %q %v", recorded, err)
		}
		if source != "" && recorded != source {
			t.Fatalf("recorded source = %q, want %q", recorded, source)
		}
		if want := "source checkout " + recorded + ": every `sisf up`"; !strings.Contains(stdout, want) {
			t.Errorf("init output does not contain %q:\n%s", want, stdout)
		}
	}
}

// recordRunLockOwner writes pid, with the time it started as ps prints it
// in the C locale, into the run lock file as the `sisf up` that holds it.
func recordRunLockOwner(lock *os.File, pid int) error {
	ps := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	ps.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	start, err := ps.Output()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"role": "up", "pid": pid, "start": strings.Join(strings.Fields(string(start)), " ")})
	if err != nil {
		return err
	}
	_, err = lock.WriteAt(raw, 0)
	return err
}
