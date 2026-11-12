package install

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func upDocker(t *testing.T) (*Installer, *fakeExec) {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	if err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	return in, exec
}

func TestFreshDockerInstallIsOneComposeProjectOfOneFactory(t *testing.T) {
	t.Parallel()
	in, exec := upDocker(t)
	m, err := in.Layout.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	proj := project(m.FactoryID)

	lines := exec.lines()
	var composeCalls []string
	for _, l := range lines {
		if strings.HasPrefix(l, "docker compose") {
			composeCalls = append(composeCalls, l)
			if !strings.Contains(l, "-p "+proj+" ") {
				t.Errorf("a compose call is not about the one project %s: %s", proj, l)
			}
		}
	}
	want := []string{"up -d --wait state", "run --rm --no-deps state-bootstrap", "up -d --wait"}
	if len(composeCalls) != len(want) {
		t.Fatalf("compose calls = %v", composeCalls)
	}
	for i, w := range want {
		if !strings.Contains(composeCalls[i], w) {
			t.Errorf("compose call %d = %q, want it to run %q", i, composeCalls[i], w)
		}
	}
	// The state store is healthy and prepared before any zone starts.
	if !strings.Contains(composeCalls[2], "--profile sim") {
		t.Errorf("the simulation profile was not enabled: %s", composeCalls[2])
	}

	rendered, err := os.ReadFile(in.Layout.Compose())
	if err != nil {
		t.Fatal(err)
	}
	f := composeFileOf(t, rendered)
	if f.Name != proj {
		t.Errorf("Compose project = %q, want %q", f.Name, proj)
	}
	ids := map[string]bool{string(m.FactoryID): true}
	for _, s := range f.Services {
		if id := s.Environment["SISF_FACTORY_ID"]; id != "" {
			ids[id] = true
		}
	}
	if len(ids) != 1 {
		t.Errorf("the project names %d factory ids: %v", len(ids), ids)
	}
	if vs, err := CheckCompose(in.Layout, rendered); err != nil || len(vs) != 0 {
		t.Errorf("rendered project breaks isolation rules: %v %v", vs, err)
	}
	report, err := conformance.CheckFile(in.Layout.Config())
	if err != nil || report.Err() != nil {
		t.Errorf("pinned manifest is not conformant: %v %v", err, report.Findings)
	}
	for _, name := range []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitDispatcher, manifest.UnitEdge, manifest.UnitSupervisor, manifest.UnitState} {
		if isUnbuilt(m.Units[name].Digest) {
			t.Errorf("unit %s is not pinned after up", name)
		}
	}
}

func TestEveryDockerCommandNamesOnlyThingsOfThisInstall(t *testing.T) {
	t.Parallel()
	in, exec := upDocker(t)
	m, _ := in.Layout.LoadManifest()
	proj := project(m.FactoryID)
	tagged := regexp.MustCompile(`\s-t\s(\S+)`)
	for _, l := range exec.lines() {
		if strings.HasPrefix(l, "docker build") {
			if tag := tagged.FindStringSubmatch(l); tag == nil || !strings.HasPrefix(tag[1], proj+"-") {
				t.Errorf("image tag outside this install's prefix: %s", l)
			}
		}
		for _, danger := range []string{"docker rm", "docker stop", "docker kill", "docker volume", "docker network", "docker system", "prune"} {
			if strings.Contains(l, danger) {
				t.Errorf("up runs %q", l)
			}
		}
		if strings.HasPrefix(l, "docker run") && !strings.Contains(l, "--name "+proj+"-") {
			t.Errorf("a throwaway container is not named with the prefix: %s", l)
		}
	}
}

func TestDockerImagesAreReferencedByTheDigestOfTheBuild(t *testing.T) {
	t.Parallel()
	in, _ := upDocker(t)
	rendered, _ := os.ReadFile(in.Layout.Compose())
	f := composeFileOf(t, rendered)
	for _, name := range []string{"app", "control", "dispatcher", "edge", "supervisor"} {
		if img := f.Services[name].Image; !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(img) {
			t.Errorf("%s runs %q, want the image ID of the build", name, img)
		}
	}
	if f.Services["control"].Image != f.Services["dispatcher"].Image {
		t.Error("control and dispatcher are one program and must run one image")
	}
	if f.Services["supervisor"].Image == f.Services["control"].Image {
		t.Error("the supervisor shares an image with a candidate unit")
	}
}

func TestUpStartsNothingWhenAnIsolationCheckFails(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	mustInit(t, in, manifest.ModeDocker)
	// An owner token anyone can read breaks the secrets rule.
	if err := os.Chmod(in.Layout.Secret(areaControl, "owner-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)})
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodePolicyHold {
		t.Fatalf("Up = %v, want POLICY_HOLD", err)
	}
	for _, l := range exec.lines() {
		if strings.HasPrefix(l, "docker compose") {
			t.Fatalf("a compose command ran though the checks failed: %s", l)
		}
	}
	if _, err := os.Stat(in.Layout.Compose()); err == nil {
		t.Fatal("a Compose file that failed its checks was written")
	}
}

func TestBootstrapCommandCarriesTheBudgetConfirmationAndNoSecret(t *testing.T) {
	t.Parallel()
	in, _ := upDocker(t)
	m, _ := in.Layout.LoadManifest()
	cmd, err := in.bootstrapCommand(m)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--confirm-unlimited --alert-micros=50000000") {
		t.Errorf("bootstrap command = %s", joined)
	}
	for _, secret := range []string{"password", "postgres://"} {
		if strings.Contains(strings.ToLower(joined), secret) {
			t.Errorf("bootstrap command carries %q", secret)
		}
	}
}

func TestDockerStopRestartAndDownActOnTheOneProject(t *testing.T) {
	t.Parallel()
	in, exec := upDocker(t)
	before := len(exec.lines())
	for _, step := range []struct {
		run  func() error
		want string
	}{
		{func() error { return in.Stop(t.Context()) }, "stop"},
		{func() error { return in.Restart(t.Context()) }, "restart"},
		{func() error { return in.Down(t.Context(), false) }, "down --remove-orphans"},
		{func() error { return in.Down(t.Context(), true) }, "down --remove-orphans --volumes"},
	} {
		if err := step.run(); err != nil {
			t.Fatal(err)
		}
		l := exec.lines()
		last := l[len(l)-1]
		if !strings.HasPrefix(last, "docker compose -p sisf-hello-app ") || !strings.HasSuffix(last, step.want) {
			t.Errorf("%s ran %q", step.want, last)
		}
	}
	if got := len(exec.lines()) - before; got != 4 {
		t.Errorf("four lifecycle commands ran %d programs", got)
	}
	// Removing volumes never touches the install's secrets or manifest.
	if _, err := os.Stat(in.Layout.Config()); err != nil {
		t.Error("down removed the manifest")
	}
}

func TestDebugPublishesThePostgresPortOnLoopbackOnly(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, func(o *Options) { o.Debug = true })
	f := composeFileOf(t, out)
	if got := f.Services["state"].Ports; len(got) != 1 || got[0] != "127.0.0.1:7469:5432" {
		t.Fatalf("state ports with --debug = %v", got)
	}
	_, plain := renderCompose(t, nil)
	if got := composeFileOf(t, plain).Services["state"].Ports; len(got) != 0 {
		t.Fatalf("state ports without --debug = %v", got)
	}
}

func TestTheSupervisorIsMeasuredInAContainerWithNothingMountedAndNoNetwork(t *testing.T) {
	t.Parallel()
	_, exec := upDocker(t)
	var measure string
	for _, l := range exec.lines() {
		if strings.HasPrefix(l, "docker run") {
			measure = l
		}
	}
	for _, want := range []string{"--rm", "--network none", "--read-only", "--cap-drop ALL", "--entrypoint supervisor", " digest"} {
		if !strings.Contains(measure, want) {
			t.Errorf("the measuring run lacks %q: %s", want, measure)
		}
	}
	for _, banned := range []string{"-v ", "--volume", "--mount", "docker.sock", "--privileged"} {
		if strings.Contains(measure, banned) {
			t.Errorf("the measuring run has %q: %s", banned, measure)
		}
	}
}
