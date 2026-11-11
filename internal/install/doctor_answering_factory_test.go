package install

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// otherFactoryNet is a host on which another factory, sisf-other, answers on
// the fixed ports: every zone healthy, the fake GitHub reachable.
func otherFactoryNet(factory string) *fakeNet {
	n := healthyNet()
	n.routes["127.0.0.1:7463/healthz"] = reply{code: 200,
		body: `{"factory_id":"` + factory + `","status":"ok","zones":{"application":"ok","agent":"ok","control":"ok"}}`}
	return n
}

// supervisorOf answers as the supervisor of factory, with a release signed
// by keyID and a last known good release recorded.
func supervisorOf(factory core.FactoryID, keyID string) func(context.Context, string) (supervisor.Status, error) {
	return func(ctx context.Context, addr string) (supervisor.Status, error) {
		s, err := supervisorWith(true)(ctx, addr)
		s.FactoryID, s.SelfCheck.ManifestKeyID = factory, keyID
		return s, err
	}
}

// noLiveCheckPasses fails the test when a check that needs this install's
// running factory reports OK.
func noLiveCheckPasses(t *testing.T, r Report) {
	t.Helper()
	for _, name := range []string{"zone application", "zone agent", "zone control", "edge to app", "dispatcher", "rollback", "edge admin"} {
		for _, v := range verdictOf(r, name) {
			if v == VerdictOK {
				t.Errorf("%s is OK: another factory's health was reported as this install's (%s)", name, detailOf(r, name))
			}
		}
	}
	if r.OK() {
		t.Error("the report passed")
	}
}

func TestTheDoctorNeverReportsAnotherFactorysHealthAsThisInstalls(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, otherFactoryNet("sisf-other"), nil)
	markStopped(in) // no `sisf up` of this install runs
	in.SupervisorStatus = supervisorOf("sisf-other", "ed25519:other")
	r := runDoctor(t, in)
	noLiveCheckPasses(t, r)
	var out bytes.Buffer
	if err := r.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "factory sisf-other") || !strings.Contains(out.String(), "not running") {
		t.Errorf("the report does not say that another factory answers:\n%s", out.String())
	}
}

func TestAnotherInstallOfTheSameFactoryIDIsToldApartByItsRootKey(t *testing.T) {
	t.Parallel()
	// Two installs of acme/hello-app on one host share the factory id; the
	// key that signed the supervisor's release tells them apart.
	in := builtStandalone(t, healthyNet(), nil)
	markStopped(in)
	in.SupervisorStatus = supervisorOf("sisf-hello-app", "ed25519:another-install")
	r := runDoctor(t, in)
	noLiveCheckPasses(t, r)
	if d := detailOf(r, runningCheck); !strings.Contains(d, "another install of factory sisf-hello-app") {
		t.Errorf("running = %q, want the other install named", d)
	}

	// The same holds while this install runs: its ports are taken.
	running := builtStandalone(t, healthyNet(), nil)
	running.SupervisorStatus = supervisorOf("sisf-hello-app", "ed25519:another-install")
	r = runDoctor(t, running)
	noLiveCheckPasses(t, r)
	if v := verdictOf(r, runningCheck); len(v) != 1 || v[0] != VerdictFail {
		t.Errorf("running = %v, want a failure", v)
	}
}

func TestAProgramThatDoesNotNameItsFactoryIsNotThisInstall(t *testing.T) {
	t.Parallel()
	net := healthyNet().with("127.0.0.1:7463/healthz", reply{code: 200, body: `{"status":"ok","zones":{"application":"ok","agent":"ok","control":"ok"}}`})
	in := builtStandalone(t, net, nil)
	markStopped(in)
	r := runDoctor(t, in)
	noLiveCheckPasses(t, r)
	if d := detailOf(r, runningCheck); !strings.Contains(d, "does not say which factory") {
		t.Errorf("running = %q", d)
	}
}

func TestAFactoryThatWasNeverStartedIsOneFailureWithTheStartCommand(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.HTTP = &http.Client{Transport: &fakeNet{routes: map[string]reply{}}}
	in.SupervisorStatus = func(context.Context, string) (supervisor.Status, error) {
		return supervisor.Status{}, os.ErrNotExist
	}
	r := runDoctor(t, in)
	var fails []string
	for _, c := range r.Checks {
		if c.Verdict == VerdictFail {
			fails = append(fails, c.Name)
		}
	}
	if len(fails) != 1 || fails[0] != runningCheck {
		t.Fatalf("failures = %v, want only %q", fails, runningCheck)
	}
	if d := detailOf(r, runningCheck); !strings.Contains(d, "sisf up --dir "+in.Layout.Dir) {
		t.Errorf("running = %q, want the start command", d)
	}
	for _, name := range []string{"release manifest", "zones", "github", "state store", "rollback"} {
		if v := verdictOf(r, name); len(v) != 1 || v[0] != VerdictSkip {
			t.Errorf("%s = %v, want skipped", name, v)
		}
	}
	if got := r.Summary(); !strings.Contains(got, "1 failure,") || strings.Contains(got, "1 failures") {
		t.Errorf("summary = %q, want the singular", got)
	}
}

func TestTheReportIsWrappedAndCarriesNoErrorCodes(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	var out bytes.Buffer
	if err := runDoctor(t, in).Write(&out); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > reportWidth && strings.Contains(strings.TrimSpace(line[reportIndent:]), " ") {
			t.Errorf("line of %d columns: %q", len(line), line)
		}
	}
	empty, _, _ := newTestInstaller(t)
	out.Reset()
	if err := runDoctor(t, empty).Write(&out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "NOT_FOUND") || !strings.Contains(out.String(), "sisf init") {
		t.Errorf("report of a directory without an install:\n%s", out.String())
	}
}
