package install

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// A fresh install of either mode is exactly one factory: one factory_id on
// the manifest, the signed release manifest, the Compose project and every
// unit, with all three zones declared in that one manifest and started,
// stopped and restarted by one lifecycle. Nothing here builds an image or a
// program: fakes stand in for go and docker.

func TestFreshStandaloneInstallIsOneFactoryAcrossAllThreeZones(t *testing.T) {
	t.Parallel()
	r := startStandalone(t, nil)
	r.stop(t)
	// up created the install, so it names the checkout it builds from.
	if out := r.in.Out.(*bytes.Buffer).String(); !strings.Contains(out, "source checkout "+r.in.SourceDir+":") {
		t.Errorf("up on a new directory does not name its source checkout:\n%s", out)
	}

	for _, p := range r.procs {
		if !p.terminated {
			t.Errorf("%s was not stopped with the rest: one lifecycle stops every unit", p.cmd.Name)
		}
	}
	m, err := r.in.Layout.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{string(m.FactoryID): true}
	signed, err := signing.ReadSignedManifest(r.in.Layout.ReleaseManifest())
	if err != nil {
		t.Fatal(err)
	}
	ids[string(signed.Manifest.FactoryID)] = true
	for _, p := range r.procs {
		for _, e := range p.cmd.Env {
			if v, ok := strings.CutPrefix(e, "SISF_FACTORY_ID="); ok {
				ids[v] = true
			}
		}
		for _, a := range p.cmd.Args {
			if v, ok := strings.CutPrefix(a, "--factory-id="); ok {
				ids[v] = true
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("the install names %d factory ids: %v", len(ids), ids)
	}
	for _, z := range core.Zones() {
		if m.Components.Zone(z) == nil {
			t.Errorf("Zone %s is not declared in the one manifest", z)
		}
	}
	report, err := conformance.CheckFile(r.in.Layout.Config())
	if err != nil || report.Err() != nil {
		t.Fatalf("pinned manifest is not conformant: %v %v", err, report.Findings)
	}
	for _, name := range []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitDispatcher, manifest.UnitEdge, manifest.UnitSupervisor} {
		if isUnbuilt(m.Units[name].Digest) {
			t.Errorf("unit %s is not pinned after up", name)
		}
	}
	if _, err := os.Stat(r.in.Layout.PIDFile()); err == nil {
		t.Error("the pid file outlived the run")
	}
}

func TestStandaloneReleaseManifestIsSignedByTheInstallsRootKeyForThisFactory(t *testing.T) {
	t.Parallel()
	r := startStandalone(t, nil)
	r.stop(t)
	pub, err := os.ReadFile(r.in.Layout.Secret(areaControl, "root-public-key"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := signing.ParsePublicKey(string(pub))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := signing.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signing.ReadSignedManifest(r.in.Layout.ReleaseManifest())
	if err != nil {
		t.Fatal(err)
	}
	rm, err := signed.Verify(t.Context(), verifier)
	if err != nil {
		t.Fatalf("release manifest does not verify under the install's root key: %v", err)
	}
	if rm.Units[manifest.UnitSupervisor] != rm.SupervisorExecutable {
		t.Error("in Standalone the supervisor's executable digest is the digest of its program file")
	}
	// A manifest edited after signing is refused.
	signed.Manifest.Units[manifest.UnitApp] = core.Digest("sha256:" + strings.Repeat("e", 64))
	if _, err := signed.Verify(t.Context(), verifier); err == nil {
		t.Fatal("a repinned unit still verified")
	}
	// And another factory's key does not verify it.
	other, _, _ := newTestInstaller(t)
	mustInit(t, other, manifest.ModeStandalone)
	otherPub, _ := os.ReadFile(other.Layout.Secret(areaControl, "root-public-key"))
	otherKey, _ := signing.ParsePublicKey(string(otherPub))
	wrong, _ := signing.NewVerifier(otherKey)
	signed2, _ := signing.ReadSignedManifest(r.in.Layout.ReleaseManifest())
	if _, err := signed2.Verify(t.Context(), wrong); err == nil {
		t.Fatal("the release manifest verified under another install's key")
	}
}

func TestStandaloneUnitsGetOnlyTheEnvironmentOfTheirOwnZone(t *testing.T) {
	t.Parallel()
	r := startStandalone(t, nil)
	r.stop(t)
	envOf := func(p *fakeProc) string { return strings.Join(p.cmd.Env, "\n") }
	control, dispatcher := envOf(r.factoryd(t, "control")), envOf(r.factoryd(t, "dispatcher"))
	for _, want := range []string{"EDGE_COUNTERS_TOKEN=", "SISF_WEBHOOK_SECRET=", "SISF_GITHUB_TOKEN=", "SISF_APPROVAL_KEY="} {
		if !strings.Contains(control, want) {
			t.Errorf("control lacks %s", want)
		}
		if strings.Contains(dispatcher, want) {
			t.Errorf("the dispatcher, Zone 2, was given %s", want)
		}
	}
	// Control signs in at the fake identity provider of the simulation and
	// reads the edge's counters; it never holds the edge's admin token.
	for _, want := range []string{"SISF_EXTERNAL_URL=http://127.0.0.1:7463", "SISF_OWNER_IDENTITIES=http://127.0.0.1:7468#owner",
		"SISF_AUTH_OIDC_ISSUER=http://127.0.0.1:7468", "SISF_SUPERVISOR_ADMIN_ADDR=127.0.0.1:7464",
		"SISF_EDGE_COUNTERS_URL=http://127.0.0.1:7469", "SISF_LOCAL_STORE_DIR=" + r.in.Layout.Data("control", "secrets")} {
		if !strings.Contains(control, want) {
			t.Errorf("control lacks %s", want)
		}
	}
	for _, banned := range []string{"EDGE_ADMIN_TOKEN=", "SISF_EDGE_ADMIN_URL=", "SISF_OWNER_TOKEN="} {
		if strings.Contains(control, banned) {
			t.Errorf("control was given %s", banned)
		}
	}
	// The edge holds its admin and counters tokens and nothing else.
	edge := r.proc(t, "sisf-edge").cmd.Env
	if len(edge) != 2 || !strings.HasPrefix(edge[0], "EDGE_ADMIN_TOKEN=") || !strings.HasPrefix(edge[1], "EDGE_COUNTERS_TOKEN=") {
		t.Errorf("the edge was given %d variables, want its admin and counters tokens alone", len(edge))
	}
	fakeOIDC := false
	for _, p := range r.procs {
		fakeOIDC = fakeOIDC || strings.Join(p.cmd.Args, " ") == "sim oidc --addr=127.0.0.1:7468"
	}
	if !fakeOIDC {
		t.Error("the simulation runs no fake identity provider on 127.0.0.1:7468")
	}
	// The supervisor holds what releasing needs; the dispatcher none of it.
	supervisor := envOf(r.proc(t, "sisf-supervisor"))
	for _, want := range []string{"SISF_GITHUB_TOKEN=", "EDGE_ADMIN_TOKEN=", "SISF_APPROVAL_PUBLIC_KEY=", "SISF_STATE_DSN=", "SISF_DISPATCHER_ADMIN_TOKEN="} {
		if !strings.Contains(supervisor, want) {
			t.Errorf("the supervisor lacks %s", want)
		}
	}
	for _, banned := range []string{"EDGE_ADMIN_TOKEN=", "SISF_APPROVAL_PUBLIC_KEY="} {
		if strings.Contains(dispatcher, banned) {
			t.Errorf("the dispatcher, Zone 2, was given %s", banned)
		}
	}
	for _, p := range r.procs {
		if !p.cmd.OnlyEnv {
			t.Errorf("%s inherits the environment of the caller", p.cmd.Name)
		}
		if !strings.HasPrefix(p.cmd.Dir, r.in.Layout.Dir) {
			t.Errorf("%s runs outside the install directory: %s", p.cmd.Name, p.cmd.Dir)
		}
	}
}

func TestStandaloneBindsEveryUnitToLoopbackOnTheFixedPorts(t *testing.T) {
	t.Parallel()
	r := startStandalone(t, nil)
	r.stop(t)
	text := ""
	for _, p := range r.procs {
		text += strings.Join(p.cmd.Args, " ") + " " + strings.Join(p.cmd.Env, " ") + "\n"
	}
	for _, want := range []string{"127.0.0.1:7460", "127.0.0.1:7461", "127.0.0.1:7463", "127.0.0.1:7464", "127.0.0.1:7465", "127.0.0.1:7467"} {
		if !strings.Contains(text, want) {
			t.Errorf("no unit uses %s", want)
		}
	}
	if strings.Contains(text, "0.0.0.0") {
		t.Error("a unit is told to listen on every interface")
	}
}

func TestStandaloneUpRecordsTheBudgetConfirmationForTheGateAndNothingWithoutIt(t *testing.T) {
	t.Parallel()
	check := func(confirmed bool) {
		r := startStandalone(t, func(o *UpOptions) {
			if !confirmed {
				o.Init.Budget = BudgetChoice{}
			}
		})
		r.stop(t)
		store, err := sqlstore.Open(t.Context(), sqlstore.Config{
			Dialect: sqlstore.DialectSQLite, DSN: r.in.Layout.StateDB(), FactoryID: "sisf-hello-app", Zone: core.ZoneControl,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		_, ok, err := ReadBudgetConfirmation(t.Context(), store)
		if err != nil || ok != confirmed {
			t.Errorf("confirmed=%v: the store holds a confirmation = %v (%v)", confirmed, ok, err)
		}
	}
	check(true)
	check(false)
}

func TestAnUnconfirmedInstallCanBeConfirmedOnceByALaterUp(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	o := initOptions(in, manifest.ModeStandalone)
	o.Budget = BudgetChoice{}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, _ := in.Layout.LoadManifest()
	choice := BudgetChoice{Confirmed: true, AlertOff: true}
	if err := in.confirmBudget(m, choice); err != nil {
		t.Fatal(err)
	}
	if got, _ := in.Layout.LoadManifest(); !got.Budget.AlertOff || got.Budget.AlertThresholdMicros != 0 {
		t.Fatalf("manifest budget after confirmation = %+v", got.Budget)
	}
	err := in.confirmBudget(m, BudgetChoice{Confirmed: true, AlertThresholdMicros: 1})
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodeAlreadyExists {
		t.Fatalf("a second confirmation = %v, want ALREADY_EXISTS", err)
	}
}

func TestUpOnAnExistingInstallKeepsItsModeAndProfile(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	mustInit(t, in, manifest.ModeStandalone)
	for name, init := range map[string]Options{
		"another mode":    {Mode: manifest.ModeDocker, Sim: true},
		"another profile": {Mode: manifest.ModeStandalone, Sim: false},
	} {
		if err := in.Up(t.Context(), UpOptions{Init: init}); err == nil {
			t.Errorf("%s: Up accepted a change that is fixed at init", name)
		}
	}
	if len(exec.startedNames()) != 0 {
		t.Fatal("a refused up started programs")
	}
}

func TestStandaloneUpRefusesASecondRunOfTheSameInstall(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	in.Signal = func(int, syscall.Signal) error { return nil } // every pid is alive
	in.ProcessStart = everyPIDStarted
	mustInit(t, in, manifest.ModeStandalone)
	holdRunLock(t, in, 4242, nil) // the first `up`, still building or running
	if err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeStandalone)}); err == nil {
		t.Fatal("a second up ran next to a live one")
	}
	if len(exec.startedNames()) != 0 {
		t.Fatal("programs were started by the refused up")
	}
}
