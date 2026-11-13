package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/pkg/core"
)

const edgeToken = "edge-admin-token-0123456789abcdef"

// lifecycleInstall is an install whose supervisor runs the lifecycle in
// the trusted simulation: the sample manifest, a prepared store, an edge
// admin route and the approval service's public key.
func lifecycleInstall(t *testing.T) (*install, []string) {
	t.Helper()
	inst := newInstall(t, mustOwnDigest(t))
	inst.env[killswitch.EnvStateDSN] = preparedStore(t)
	inst.env["SISF_ROOT_PRIVATE_KEY"] = inst.rootPrivate
	inst.env[envEdgeAdminToken] = edgeToken
	inst.env[envGitHubToken] = "github-token-of-the-test"
	approvalPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inst.env["SISF_APPROVAL_PUBLIC_KEY"] = signing.EncodePublicKey(approvalPub)

	sample, err := os.ReadFile(filepath.Join("..", "..", ".sisf", "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(inst.dir, "factory.yaml")
	if err := os.WriteFile(manifestPath, append(sample, []byte("\nextensions:\n  profile: sim\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	splitter, err := edge.NewSplitter(core.SystemClock{}, nil, edge.Target{Revision: "stable", URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := edge.NewAdminHandler(splitter, edgeToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(admin)
	t.Cleanup(srv.Close)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return inst, append(inst.runArgs(), "--factory-manifest", manifestPath, "--edge-admin-url", srv.URL,
		"--qa-judge", "fake", "--deploy", "local", "--deploy-local-program", program, "--root-private-key-env", "SISF_ROOT_PRIVATE_KEY")
}

// A supervisor given the factory manifest issues its delegated keys onto
// the trust volume, runs the stages and serves the Owner rollback, which
// spends an approval before anything changes.
func TestASupervisorGivenTheFactoryManifestRunsTheLifecycle(t *testing.T) {
	t.Parallel()
	inst, args := lifecycleInstall(t)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	stderr := newOutput("serving status")
	exited := make(chan int, 1)
	go func() {
		exited <- run(ctx, args, env{stdout: newOutput(""), stderr: stderr, lookupEnv: inst.lookupEnv})
	}()
	select {
	case <-stderr.seen:
	case status := <-exited:
		t.Fatalf("the supervisor exited with status %d instead of serving:\n%s", status, stderr)
	}

	for _, p := range []delegation.Purpose{delegation.PurposeQAReceipt, delegation.PurposeDeployArtifact} {
		info, err := os.Stat(filepath.Join(inst.dir, "trust", privateKeyFile(p)))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the %s key is not on the trust volume for the supervisor only: %v", p, err)
		}
	}
	client, err := supervisor.NewClient("unix:" + inst.socket)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.RequestRollback(ctx, supervisor.RollbackRequest{FactoryID: testFactory, Unit: "app", Reason: "drill", ApprovalRef: "unknown"})
	if err == nil || core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("the Owner rollback was not wired, or ran without an approval: %v", err)
	}
	interrupt()
	if status := <-exited; status != exitOK {
		t.Fatalf("the supervisor exited %d:\n%s", status, stderr)
	}
	for _, secret := range []string{edgeToken, inst.env[envGitHubToken], "PRIVATE KEY"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatal("a secret reached the supervisor's log")
		}
	}
}

// A lifecycle that cannot be wired refuses the start instead of running a
// supervisor that cannot release. A refusal of the configuration itself
// ends with EX_CONFIG, which `sisf up` does not restart on; a missing file
// may be there at the next start.
func TestASupervisorWhoseLifecycleCannotBeWiredRefusesToStart(t *testing.T) {
	t.Parallel()
	type refusal struct {
		change func(i *install, args []string) []string
		want   core.ErrorCode
	}
	for name, c := range map[string]refusal{
		"no edge token":      {func(i *install, a []string) []string { delete(i.env, envEdgeAdminToken); return a }, core.CodeNotFound},
		"no approval key":    {func(i *install, a []string) []string { delete(i.env, "SISF_APPROVAL_PUBLIC_KEY"); return a }, core.CodeNotFound},
		"no delegated keys":  {func(_ *install, a []string) []string { return a[:len(a)-2] }, core.CodeNotFound},
		"no state store":     {func(i *install, a []string) []string { delete(i.env, killswitch.EnvStateDSN); return a }, core.CodeInvalidArgument},
		"a relative program": {func(_ *install, a []string) []string { return append(a, "--deploy-local-program", "sh") }, core.CodeInvalidArgument},
		// Untrusted code is never built on the local backend.
		"untrusted local exec": {func(_ *install, a []string) []string { return append(a, "--exec", "local", "--coder-agent", "codex") }, core.CodePermissionDenied},
		"no build backend":     {func(_ *install, a []string) []string { return append(a, "--coder-agent", "claude") }, core.CodeNotSupported},
		// QA is never skipped: the agent judge needs a backend to run under.
		"the agent judge without a backend": {func(_ *install, a []string) []string { return append(a, "--qa-judge", "agent") }, core.CodeNotSupported},
		"an unknown judge":                  {func(_ *install, a []string) []string { return append(a, "--qa-judge", "lenient") }, core.CodeInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inst, args := lifecycleInstall(t)
			status, _, stderr := invoke(inst.lookupEnv, c.change(inst, args)...)
			wantStatus := exitConfigRefused
			if c.want == core.CodeNotFound {
				wantStatus = exitFailure
			}
			if status != wantStatus || !strings.Contains(stderr, "lifecycle") || !strings.Contains(stderr, string(c.want)) {
				t.Fatalf("status %d, stderr %q, want status %d and a %s refusal naming the lifecycle", status, stderr, wantStatus, c.want)
			}
			if inst.socketExists() {
				t.Error("a refused supervisor opened its admin socket")
			}
		})
	}
}
