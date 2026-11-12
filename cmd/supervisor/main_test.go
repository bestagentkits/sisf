package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

const (
	testFactory = "acme-hello"
	otherDigest = core.Digest("sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
)

// ownDigest measures the test binary once. Inside a test the test binary is
// the "supervisor" that runs, so a manifest pinning this digest passes the
// self-check for real, with nothing injected.
var ownDigest = sync.OnceValues(supervisor.MeasureExecutable)

// output collects what a command wrote. It is safe to write from the
// goroutine that runs the command and to read from the test. When marker
// appears in the output, seen is closed, which lets a test wait for the
// supervisor to be up without sleeping.
type output struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	seen   chan struct{}
	once   sync.Once
}

func newOutput(marker string) *output {
	return &output{marker: marker, seen: make(chan struct{})}
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(p)
	if o.marker != "" && strings.Contains(o.buf.String(), o.marker) {
		o.once.Do(func() { close(o.seen) })
	}
	return n, err
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// rootKeyVariable is the environment variable the installations name as the
// root key reference.
const rootKeyVariable = "SISF_ROOT_PUBLIC_KEY"

// install is one installed supervisor: a root key generated for the test,
// whose public half is in the environment, a signed release manifest and a
// trust directory.
type install struct {
	t      *testing.T
	dir    string
	socket string
	pinned core.Digest
	env    map[string]string
	// rootPrivate is the PEM of the root private key, for a test of the
	// local native profile, where the supervisor issues its own keys.
	rootPrivate string
}

// lookupEnv is the environment the supervisor of this installation sees.
func (i *install) lookupEnv(name string) (string, bool) {
	value, ok := i.env[name]
	return value, ok
}

// newInstall writes an installation whose manifest pins the given digest as
// the supervisor's executable.
func newInstall(t *testing.T, pinned core.Digest) *install {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "trust"), 0o700); err != nil {
		t.Fatalf("create trust directory: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signed, err := signing.SignManifest(context.Background(), signer, signing.ReleaseManifest{
		APIVersion:           signing.ReleaseAPIVersion,
		FactoryID:            testFactory,
		IssuedAt:             time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		Units:                map[manifest.UnitName]core.Digest{manifest.UnitSupervisor: pinned},
		SupervisorExecutable: pinned,
	})
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := signing.WriteSignedManifest(filepath.Join(dir, "release.json"), signed); err != nil {
		t.Fatalf("WriteSignedManifest: %v", err)
	}
	// The per-test directory can be too long a path for a Unix socket.
	socketDir, err := os.MkdirTemp("", "sv")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	encoded, err := signing.EncodePrivateKey(priv)
	if err != nil {
		t.Fatalf("EncodePrivateKey: %v", err)
	}
	return &install{t: t, dir: dir, socket: filepath.Join(socketDir, "admin.sock"), pinned: pinned,
		env: map[string]string{rootKeyVariable: signing.EncodePublicKey(pub)}, rootPrivate: string(encoded)}
}

// runArgs is the command line that starts this installation's supervisor.
func (i *install) runArgs() []string {
	return []string{"run",
		"--factory-id", testFactory,
		"--manifest", filepath.Join(i.dir, "release.json"),
		"--root-key-ref", "env:" + rootKeyVariable,
		"--trust-dir", filepath.Join(i.dir, "trust"),
		"--admin", "unix:" + i.socket,
	}
}

// socketExists reports whether the admin socket is on disk.
func (i *install) socketExists() bool {
	_, err := os.Lstat(i.socket)
	return !errors.Is(err, fs.ErrNotExist)
}

// noEnv is an empty environment.
func noEnv(string) (string, bool) { return "", false }

// invoke runs one command to completion in the given environment and
// returns its exit status and output.
func invoke(lookupEnv func(string) (string, bool), args ...string) (status int, stdout, stderr string) {
	out, errOut := newOutput(""), newOutput("")
	status = run(context.Background(), args, env{stdout: out, stderr: errOut, lookupEnv: lookupEnv})
	return status, out.String(), errOut.String()
}

func mustOwnDigest(t *testing.T) core.Digest {
	t.Helper()
	digest, err := ownDigest()
	if err != nil {
		t.Fatalf("measure the test binary: %v", err)
	}
	return digest
}

func TestSupervisorRunsServesStatusAndStopsWhenInterrupted(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, mustOwnDigest(t))
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	stderr := newOutput("serving status")
	exited := make(chan int, 1)
	go func() {
		exited <- run(ctx, inst.runArgs(), env{stdout: newOutput(""), stderr: stderr, lookupEnv: inst.lookupEnv})
	}()

	select {
	case <-stderr.seen:
	case status := <-exited:
		t.Fatalf("the supervisor exited with status %d instead of serving:\n%s", status, stderr)
	}

	status, stdout, errText := invoke(noEnv, "status", "--admin", "unix:"+inst.socket)
	if status != exitOK {
		t.Fatalf("status exited %d: %s", status, errText)
	}
	var reported supervisor.Status
	if err := json.Unmarshal([]byte(stdout), &reported); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, stdout)
	}
	if reported.FactoryID != testFactory || !reported.SelfCheck.Passed || reported.SelfCheck.ExecutableDigest != inst.pinned {
		t.Errorf("reported status = %+v, want the passed self check of %s", reported, inst.pinned)
	}

	interrupt()
	if status := <-exited; status != exitOK {
		t.Errorf("the supervisor exited %d after an interrupt, want 0:\n%s", status, stderr)
	}
	if inst.socketExists() {
		t.Error("the admin socket was left behind")
	}
}

func TestSupervisorExitsWithFailureWhenTheManifestPinsAnotherDigest(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, otherDigest)
	status, _, stderr := invoke(inst.lookupEnv, inst.runArgs()...)
	if status != exitFailure {
		t.Fatalf("exit status = %d, want %d:\n%s", status, exitFailure, stderr)
	}
	if !strings.Contains(stderr, "refusing to start") || !strings.Contains(stderr, string(core.CodeTampered)) {
		t.Errorf("stderr does not say why the supervisor refused:\n%s", stderr)
	}
	if inst.socketExists() {
		t.Error("a refused supervisor opened its admin socket")
	}
}

func TestSupervisorExitsWithFailureWithoutTheRootKey(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, mustOwnDigest(t))
	delete(inst.env, rootKeyVariable)
	status, _, stderr := invoke(inst.lookupEnv, inst.runArgs()...)
	if status != exitFailure || !strings.Contains(stderr, string(core.CodeNotFound)) {
		t.Fatalf("exit status = %d, want %d with NOT_FOUND:\n%s", status, exitFailure, stderr)
	}
	if inst.socketExists() {
		t.Error("a refused supervisor opened its admin socket")
	}
}

func TestSupervisorWithMissingSettingsRefusesInsteadOfGuessing(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, mustOwnDigest(t))
	full := inst.runArgs()
	// Drop each required setting in turn; "--admin" alone has a default.
	for _, flagName := range []string{"--factory-id", "--manifest", "--root-key-ref", "--trust-dir"} {
		var args []string
		for i := 0; i < len(full); i++ {
			if full[i] == flagName {
				i++
				continue
			}
			args = append(args, full[i])
		}
		status, _, stderr := invoke(inst.lookupEnv, args...)
		if status != exitFailure || !strings.Contains(stderr, "refusing to start") {
			t.Errorf("without %s: exit status = %d, want a refusal:\n%s", flagName, status, stderr)
		}
	}
	if inst.socketExists() {
		t.Error("a refused supervisor opened its admin socket")
	}
}

func TestDigestCommandPrintsWhatTheSelfCheckMeasures(t *testing.T) {
	t.Parallel()
	status, stdout, stderr := invoke(noEnv, "digest")
	if status != exitOK || stdout != string(mustOwnDigest(t))+"\n" {
		t.Fatalf("digest = %d %q (%s), want %q", status, stdout, stderr, mustOwnDigest(t))
	}
}

func TestStatusCommandFailsWhenNoSupervisorAnswers(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, otherDigest)
	status, stdout, stderr := invoke(noEnv, "status", "--admin", "unix:"+inst.socket)
	if status != exitFailure || stdout != "" || !strings.Contains(stderr, string(core.CodeUnavailable)) {
		t.Errorf("status = %d %q, want failure with UNAVAILABLE:\n%s", status, stdout, stderr)
	}
	status, _, stderr = invoke(noEnv, "status", "--admin", "192.168.1.10:7464")
	if status != exitFailure || !strings.Contains(stderr, string(core.CodeInvalidArgument)) {
		t.Errorf("status on a remote address = %d, want it refused as not local:\n%s", status, stderr)
	}
}

func TestWrongInvocationIsAUsageError(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no command":           nil,
		"unknown command":      {"release"},
		"unknown flag":         {"run", "--skip-self-check"},
		"stray argument":       {"run", "now"},
		"stray digest arg":     {"digest", "file"},
		"flag without a value": {"status", "--admin"},
	} {
		if status, stdout, _ := invoke(noEnv, args...); status != exitUsage || stdout != "" {
			t.Errorf("%s: exit status = %d with stdout %q, want %d and no output", name, status, stdout, exitUsage)
		}
	}
	for _, help := range [][]string{{"help"}, {"--help"}, {"run", "--help"}} {
		if status, _, _ := invoke(noEnv, help...); status != exitOK {
			t.Errorf("%v: exit status = %d, want 0", help, status)
		}
	}
	if _, stdout, _ := invoke(noEnv, "help"); !strings.Contains(stdout, "supervisor run") {
		t.Errorf("help does not describe the run command:\n%s", stdout)
	}
}
