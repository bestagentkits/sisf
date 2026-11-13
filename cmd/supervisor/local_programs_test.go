package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const launcherPath = "/srv/factory/bin/sisf-app-launcher"

// exported keeps data in the store as a build's exported program.
func exported(t *testing.T, store *programstore.Store, data string) core.Digest {
	t.Helper()
	in, err := store.Receive()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Abort()
	if _, err := in.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	digest, err := in.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func digestOfText(data string) core.Digest {
	sum := sha256.Sum256([]byte(data))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// trustStore is a program store on a new trust volume, and the volume.
func trustStore(t *testing.T) (*programstore.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := programstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store, dir
}

// Outside the simulation the local deploy backend starts, for a release,
// the program the store holds under the release's digest, and that file
// exactly; for the digest the root-signed release manifest pins it starts
// the installed program; and for anything else it starts nothing.
func TestTheLocalBackendRunsTheProgramOfTheReleasesDigestOrNothing(t *testing.T) {
	t.Parallel()
	store, _ := trustStore(t)
	candidate := exported(t, store, "the candidate's own build")
	installed := digestOfText("the installed program")
	p := &localPrograms{launcher: launcherPath, store: store, installed: installed}

	path, args, err := p.command("smoke.local/app@" + string(candidate))
	if err != nil {
		t.Fatalf("a release whose program is in the store: %v", err)
	}
	want, _ := store.Path(candidate)
	if path != launcherPath || len(args) != 2 || args[0] != "smoke.local/app@"+string(candidate) || args[1] != want {
		t.Errorf("started %s %v, want the launcher with the store's file %s", path, args, want)
	}
	if len(args) == 2 {
		if kept, err := os.ReadFile(args[1]); err != nil || string(kept) != "the candidate's own build" {
			t.Errorf("the file it starts holds %q (%v)", kept, err)
		}
	}

	path, args, err = p.command("local/acme-hello/app@" + string(installed))
	if err != nil || path != launcherPath || len(args) != 1 {
		t.Errorf("the installed release: %s %v, %v; want the launcher with no program of the store", path, args, err)
	}

	for name, image := range map[string]string{
		"a digest no build exported":    "smoke.local/app@" + string(digestOfText("never built")),
		"an image not pinned":           "smoke.local/app:latest",
		"a digest that is not a sha256": "smoke.local/app@sha256:1234",
		"an option":                     "--help@" + string(candidate),
	} {
		if path, args, err := p.command(image); err == nil {
			t.Errorf("%s was started as %s %v, want it refused", name, path, args)
		}
	}
	// Refusals name no fall back: the installed program is never started
	// under a digest that is not its own.
	_, _, err = p.command("smoke.local/app@" + string(digestOfText("never built")))
	if !core.HasCode(err, core.CodeNotFound) || !strings.Contains(err.Error(), "nothing is started") {
		t.Errorf("an unknown release: %v", err)
	}
}

// The store is hashed again at every start: a program whose bytes changed
// after it was received is refused, and the installed program is not
// started in its place.
func TestAStoredProgramThatChangedIsNotStarted(t *testing.T) {
	t.Parallel()
	store, _ := trustStore(t)
	candidate := exported(t, store, "the program QA judged")
	// Even when the installed release has the same digest.
	p := &localPrograms{launcher: launcherPath, store: store, installed: candidate}
	_, args, err := p.command("smoke.local/app@" + string(candidate))
	if err != nil || len(args) != 2 {
		t.Fatalf("a release whose program is in the store starts %v (%v)", args, err)
	}
	if err := os.Chmod(args[1], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[1], []byte("what an agent swapped in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if path, args, err := p.command("smoke.local/app@" + string(candidate)); !core.HasCode(err, core.CodeTampered) {
		t.Errorf("a changed program: started %s %v, %v; want TAMPERED", path, args, err)
	}
}

// In the simulation no build makes a program: every release starts the
// installed one, as before.
func TestTheSimulationStartsTheInstalledProgramForEveryRelease(t *testing.T) {
	t.Parallel()
	p := &localPrograms{launcher: launcherPath}
	path, args, err := p.command("sim.local/acme-hello/app@" + string(digestOfText("a simulated image")))
	if err != nil || path != launcherPath || len(args) != 1 {
		t.Errorf("a simulated release: %s %v, %v", path, args, err)
	}
	if _, _, err := p.command("-rf"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an option as the image: %v", err)
	}
}

// Only a factory whose local backend deploys sandboxed builds keeps
// programs: the simulation with its scripted agent and the Compose backend
// are wired without a store, so their builds export nothing.
func TestOnlyALocalBackendOutsideTheSimulationKeepsPrograms(t *testing.T) {
	t.Parallel()
	_, trust := trustStore(t)
	book, _ := installedBook(t, digestOfText("the installed program"))
	for name, tc := range map[string]struct {
		deploy, agent string
		sim, keeps    bool
	}{
		"standalone with real builds":  {deploy: "local", agent: "claude", keeps: true},
		"standalone, scripted coder":   {deploy: "local", agent: "fake", keeps: true},
		"the simulation":               {deploy: "local", agent: "fake", sim: true},
		"compose":                      {deploy: "compose", agent: "claude"},
		"compose in the simulation":    {deploy: "compose", agent: "fake", sim: true},
		"the simulation, a real agent": {deploy: "local", agent: "claude", sim: true, keeps: true},
	} {
		l := &lifecycleFlags{deploy: tc.deploy, coderAgent: tc.agent, localProgram: launcherPath}
		if err := l.openLocalPrograms(context.Background(), trust, tc.sim, book); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := l.programs.buildStore() != nil; got != tc.keeps {
			t.Errorf("%s: keeps programs %t, want %t", name, got, tc.keeps)
		}
		if tc.keeps && l.programs.installed != digestOfText("the installed program") {
			t.Errorf("%s: the installed release is %q, not the one the root key signed", name, l.programs.installed)
		}
	}
}

// promote writes the entry the supervisor keeps of a release it promoted.
func promote(t *testing.T, trust string, digest core.Digest, image string) {
	t.Helper()
	entry, err := json.Marshal(release.LKG{Unit: adoptedUnit, Digest: core.ReleaseDigest(digest), Image: image,
		Source: release.SourcePromoted, VerifiedAt: time.Now().UTC(), RecordedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trust, "lkg-unit-"+adoptedUnit+".json"), entry, 0o600); err != nil {
		t.Fatal(err)
	}
}

// After a promotion a supervisor that starts again serves the promoted
// program: the last known good release it adopts is the promoted one, and
// the local backend resolves it to the store's file, not to the installed
// program.
func TestARestartedSupervisorStartsThePromotedProgram(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	installed := digestOfText("the installed program")
	trust := filepath.Join(t.TempDir(), "trust")
	book, signer := installedBookIn(t, trust, installed)
	store, err := programstore.Open(trust)
	if err != nil {
		t.Fatal(err)
	}
	promoted := exported(t, store, "the promoted candidate")
	promote(t, trust, promoted, "smoke.local/app@"+string(promoted))

	// A new supervisor process: nothing in memory, only the trust volume.
	l := &lifecycleFlags{deploy: "local", coderAgent: "claude", localProgram: launcherPath}
	if err := l.openLocalPrograms(ctx, trust, false, book); err != nil {
		t.Fatal(err)
	}
	backend := &adoptionBackend{}
	a := &lkgAdopter{factory: testFactory, backend: backend, lkg: book, signer: signer, image: "local/" + string(testFactory) + "/app"}
	if err := a.adopt(ctx); err != nil {
		t.Fatal(err)
	}
	if len(backend.staged) != 1 || backend.staged[0].Digest != core.ReleaseDigest(promoted) {
		t.Fatalf("the restarted supervisor staged %+v, want the promoted release", backend.staged)
	}
	_, args, err := l.programs.command(backend.staged[0].Image)
	want, _ := store.Path(promoted)
	if err != nil || len(args) != 2 || args[1] != want {
		t.Errorf("the promoted release starts %v (%v), want the store's file %s", args, err, want)
	}
}

// The release pipeline verifies what the local backend would start, hashed
// now: a kept program measures as its bytes, so one swapped after it was
// received is not the signed build; the installed release is the digest the
// root-signed manifest gives; anything else is not found, and a factory that
// keeps no programs observes nothing.
func TestTheBytesOfAReleaseAreMeasuredNotTakenFromTheArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := trustStore(t)
	candidate := exported(t, store, "the candidate's own build")
	installed := digestOfText("the installed program")
	p := &localPrograms{launcher: launcherPath, store: store, installed: installed}
	artifact := func(d core.Digest) deploy.Artifact {
		return deploy.Artifact{Unit: "app", Digest: core.ReleaseDigest(d), Image: "smoke.local/app@" + string(d)}
	}

	if got, err := p.ObserveArtifact(ctx, artifact(candidate)); err != nil || got != core.ReleaseDigest(candidate) {
		t.Errorf("a kept program: %s, %v", got, err)
	}
	if got, err := p.ObserveArtifact(ctx, artifact(installed)); err != nil || got != core.ReleaseDigest(installed) {
		t.Errorf("the installed release: %s, %v", got, err)
	}
	if _, err := p.ObserveArtifact(ctx, artifact(digestOfText("never received"))); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("a release neither kept nor installed: %v, want NOT_FOUND", err)
	}

	path, err := store.Path(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("what an agent swapped in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := p.ObserveArtifact(ctx, artifact(candidate)); err != nil || got != core.ReleaseDigest(digestOfText("what an agent swapped in")) {
		t.Errorf("a program swapped after it was received measures as %s, %v; want the bytes kept now", got, err)
	}

	none := &localPrograms{launcher: launcherPath}
	if _, err := none.ObserveArtifact(ctx, artifact(candidate)); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("a factory that keeps no programs: %v, want NOT_SUPPORTED", err)
	}
}

// Only a factory that keeps programs, or the simulation with its scripted
// agent, has an observer for its releases.
func TestOnlyAFactoryThatKeepsProgramsOrTheSimulationObservesReleases(t *testing.T) {
	t.Parallel()
	store, _ := trustStore(t)
	kept := &lifecycleFlags{coderAgent: "claude", programs: &localPrograms{launcher: launcherPath, store: store}}
	if o, err := kept.artifactObserver(false); err != nil || o != release.ArtifactObserver(kept.programs) {
		t.Errorf("a factory that keeps programs observes with %T, %v", o, err)
	}
	simulated := &lifecycleFlags{coderAgent: "fake", programs: &localPrograms{launcher: launcherPath}}
	if o, err := simulated.artifactObserver(true); err != nil {
		t.Errorf("the simulation: %T, %v", o, err)
	} else if _, ok := o.(stages.SimulatedArtifacts); !ok {
		t.Errorf("the simulation observes with %T", o)
	}
	for name, l := range map[string]struct {
		flags *lifecycleFlags
		sim   bool
	}{
		"production without a store":         {&lifecycleFlags{coderAgent: "claude", programs: &localPrograms{launcher: launcherPath}}, false},
		"the simulation with a real agent":   {&lifecycleFlags{coderAgent: "claude", programs: &localPrograms{launcher: launcherPath}}, true},
		"the scripted agent outside the sim": {&lifecycleFlags{coderAgent: "fake", programs: &localPrograms{launcher: launcherPath}}, false},
	} {
		if _, err := l.flags.artifactObserver(l.sim); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("%s: %v, want NOT_SUPPORTED", name, err)
		}
	}
}
