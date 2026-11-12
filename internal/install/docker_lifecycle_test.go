package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"gopkg.in/yaml.v3"
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
		// Listing the projects only reads, to see who owns the name.
		if strings.HasPrefix(l, "docker compose") && !strings.HasPrefix(l, "docker compose ls ") {
			composeCalls = append(composeCalls, l)
			if !strings.Contains(l, "-p "+proj+" ") {
				t.Errorf("a compose call is not about the one project %s: %s", proj, l)
			}
		}
	}
	want := []string{"up -d --wait state", "run --rm --no-deps state-bootstrap", "--profile bootstrap run --rm --no-deps trust-bootstrap", "up -d --wait"}
	if len(composeCalls) != len(want) {
		t.Fatalf("compose calls = %v", composeCalls)
	}
	for i, w := range want {
		if !strings.Contains(composeCalls[i], w) {
			t.Errorf("compose call %d = %q, want it to run %q", i, composeCalls[i], w)
		}
	}
	// The state store is healthy and prepared before any zone starts.
	if !strings.Contains(composeCalls[3], "--profile sim") {
		t.Errorf("the simulation profile was not enabled: %s", composeCalls[3])
	}

	rendered, err := os.ReadFile(in.Layout.Compose())
	if err != nil {
		t.Fatal(err)
	}
	f := composeFileOf(t, rendered)
	if f.Name != proj {
		t.Errorf("Compose project = %q, want %q", f.Name, proj)
	}
	// One factory_id on the manifest, the signed release manifest and every
	// service of the project, by environment or by flag.
	ids := map[string]bool{string(m.FactoryID): true}
	signed, err := signing.ReadSignedManifest(in.Layout.ReleaseManifest())
	if err != nil {
		t.Fatal(err)
	}
	ids[string(signed.Manifest.FactoryID)] = true
	for name, s := range f.Services {
		if id := s.Environment["SISF_FACTORY_ID"]; id != "" {
			ids[id] = true
		}
		var command []string
		if err := s.Command.Decode(&command); err != nil && !s.Command.IsZero() {
			t.Fatalf("the command of service %s cannot be read: %v", name, err)
		}
		for _, a := range command {
			if v, ok := strings.CutPrefix(a, "--factory-id="); ok {
				ids[v] = true
			}
		}
	}
	if len(ids) != 1 {
		t.Errorf("the project names %d factory ids: %v", len(ids), ids)
	}
	// All three zones are declared in the one manifest and each runs as a
	// service of this one project, so one up, stop and down moves them all.
	for _, z := range core.Zones() {
		if m.Components.Zone(z) == nil {
			t.Errorf("Zone %s is not declared in the one manifest", z)
		}
		var services []manifest.UnitName
		for name := range m.Units {
			if _, ok := f.Services[string(name)]; ok && name.Zone() == z {
				services = append(services, name)
			}
		}
		if len(services) == 0 {
			t.Errorf("Zone %s has no service in the project %s", z, proj)
		}
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
		// The one volume command is the read-only listing of this
		// project's volumes, which checks who made them.
		ownVolumes := strings.HasPrefix(l, "docker volume ls --filter label=com.docker.compose.project="+proj+" ")
		for _, danger := range []string{"docker rm", "docker stop", "docker kill", "docker volume", "docker network create", "docker network rm", "docker system", "prune"} {
			if strings.Contains(l, danger) && !ownVolumes {
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
	// A webhook secret anyone can read breaks the secrets rule.
	if err := os.Chmod(in.Layout.Secret(areaControl, "webhook-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)})
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodePolicyHold {
		t.Fatalf("Up = %v, want POLICY_HOLD", err)
	}
	for _, l := range exec.lines() {
		if strings.HasPrefix(l, "docker compose") && !strings.HasPrefix(l, "docker compose ls ") {
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
	// Each down first lists the containers the supervisor deployed for this
	// factory, which Compose does not know of; there are none here.
	var extra []string
	// Each command also first lists this project's volumes, read-only, to
	// check that this install made them.
	for _, l := range exec.lines()[before:] {
		if !strings.HasPrefix(l, "docker compose") && !strings.HasPrefix(l, "docker volume ls --filter label=com.docker.compose.project=sisf-hello-app ") {
			extra = append(extra, l)
		}
	}
	if len(extra) != 2 || extra[0] != "docker ps -aq --filter label=sisf.factory=sisf-hello-app" || extra[1] != extra[0] {
		t.Errorf("the lifecycle commands ran %v besides Compose", extra)
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
		if strings.HasPrefix(l, "docker run") && strings.Contains(l, "--entrypoint supervisor") {
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

// Two installs of same-named repositories share a factory id, and so the
// Compose project name: one never starts, stops or deletes the other's.
func TestADockerInstallNeverActsOnAnotherInstallsComposeProject(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	mustInit(t, in, manifest.ModeDocker)
	if err := os.WriteFile(in.Layout.Compose(), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := `[{"Name":"sisf-hello-app","Status":"running(6)","ConfigFiles":"/elsewhere/factory/compose.yaml"}]`
	exec.answer = func(c Cmd) ([]byte, error) {
		if strings.Join(c.Args[:2], " ") == "compose ls" {
			return []byte(other), nil
		}
		return buildAnswers(c)
	}
	for name, run := range map[string]func() error{
		"up":      func() error { return in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)}) },
		"stop":    func() error { return in.Stop(t.Context()) },
		"down -v": func() error { return in.Down(t.Context(), true) },
	} {
		if err := run(); !core.HasCode(err, core.CodeConflict) || !strings.Contains(err.Error(), "/elsewhere/factory/compose.yaml") {
			t.Errorf("%s = %v, want CONFLICT naming the other install", name, err)
		}
	}
	for _, l := range exec.lines() {
		if strings.Contains(l, " down") || strings.Contains(l, " stop") || strings.Contains(l, " up -d") {
			t.Fatalf("a command acted on the other install's project: %s", l)
		}
	}
	// Its own project is recognised through a symbolic link to its
	// directory, and a project whose name only starts with this one's is
	// another project, not a conflict.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(in.Layout.Dir, link); err != nil {
		t.Fatal(err)
	}
	own := `[{"Name":"sisf-hello-app","ConfigFiles":"` + filepath.Join(link, "compose.yaml") + `"},` +
		`{"Name":"sisf-hello-app-2","ConfigFiles":"/elsewhere/compose.yaml"}]`
	exec.answer = func(c Cmd) ([]byte, error) {
		if strings.Join(c.Args[:2], " ") == "compose ls" {
			return []byte(own), nil
		}
		return buildAnswers(c)
	}
	if err := in.Stop(t.Context()); err != nil {
		t.Errorf("stop of its own project reached through a link = %v", err)
	}
	// A Docker install that was never started stops and goes down cleanly.
	fresh, _, _ := newTestInstaller(t)
	mustInit(t, fresh, manifest.ModeDocker)
	if err := fresh.Down(t.Context(), true); err != nil {
		t.Errorf("down -v of a Docker install never started = %v", err)
	}
}

// After `sisf down` without -v a project has no container left, so Compose
// no longer lists it, but its volumes stay. Another install of the same
// factory id must not adopt them: each volume names the install that made
// it, and one made by no known install is not adopted by a new install.
func TestADockerInstallNeverAdoptsTheVolumesAnotherInstallLeftBehind(t *testing.T) {
	t.Parallel()
	volumes := func(in *Installer, exec *fakeExec, listed string) {
		exec.answer = func(c Cmd) ([]byte, error) {
			if strings.Join(c.Args[:2], " ") == "volume ls" {
				return []byte(listed), nil
			}
			return buildAnswers(c)
		}
	}
	acts := func(in *Installer) map[string]func() error {
		return map[string]func() error{
			"up":      func() error { return in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)}) },
			"stop":    func() error { return in.Stop(t.Context()) },
			"down -v": func() error { return in.Down(t.Context(), true) },
		}
	}
	noAction := func(exec *fakeExec) {
		t.Helper()
		for _, l := range exec.lines() {
			if strings.Contains(l, " down") || strings.Contains(l, " stop") || strings.Contains(l, " up ") {
				t.Fatalf("a command acted on volumes of another install: %s", l)
			}
		}
	}

	// Volumes another install made.
	in, exec, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	if err := os.WriteFile(in.Layout.Compose(), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	volumes(in, exec, "sisf-hello-app-state-data /elsewhere/my factory\nsisf-hello-app-trust /elsewhere/my factory\n")
	for name, run := range acts(in) {
		if err := run(); !core.HasCode(err, core.CodeConflict) || !strings.Contains(err.Error(), "/elsewhere/my factory") {
			t.Errorf("%s = %v, want CONFLICT naming the other install", name, err)
		}
	}
	noAction(exec)

	// Volumes no install names, met by an install that never started its
	// project: they are another install's, made before volumes named one.
	fresh, freshExec, _ := newTestInstaller(t)
	mustInit(t, fresh, manifest.ModeDocker)
	volumes(fresh, freshExec, "sisf-hello-app-state-data \n")
	if err := fresh.Up(t.Context(), UpOptions{Init: initOptions(fresh, manifest.ModeDocker)}); !core.HasCode(err, core.CodeConflict) {
		t.Errorf("up of a new install next to unnamed volumes = %v, want CONFLICT", err)
	}
	noAction(freshExec)

	// Its own volumes, named through a symbolic link to its directory, and
	// unnamed ones of a project it rendered before volumes named the install.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(in.Layout.Dir, link); err != nil {
		t.Fatal(err)
	}
	volumes(in, exec, "sisf-hello-app-state-data "+link+"\nsisf-hello-app-trust \n")
	if err := in.Stop(t.Context()); err != nil {
		t.Errorf("stop with its own volumes = %v", err)
	}
	if err := in.Down(t.Context(), true); err != nil {
		t.Errorf("down -v with its own volumes = %v", err)
	}
}

// Every volume of the rendered project names the install that made it.
func TestEveryComposeVolumeNamesTheInstallThatMadeIt(t *testing.T) {
	t.Parallel()
	in, rendered := renderCompose(t, nil)
	var file struct {
		Volumes map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(rendered, &file); err != nil || len(file.Volumes) == 0 {
		t.Fatalf("rendered volumes = %v %v", file.Volumes, err)
	}
	for name, v := range file.Volumes {
		if v.Labels[installDirLabel] != in.Layout.Dir {
			t.Errorf("volume %s labels = %v, want %s=%s", name, v.Labels, installDirLabel, in.Layout.Dir)
		}
	}
	if vs, err := CheckCompose(in.Layout, rendered); err != nil || len(vs) > 0 {
		t.Fatalf("the labelled project does not pass the isolation rules: %v %v", vs, err)
	}
}

// Control in Docker mode names the install's root key id on /healthz.
func TestDockerControlIsGivenTheRootKeyIDOfItsInstall(t *testing.T) {
	t.Parallel()
	in, rendered := renderCompose(t, nil)
	want, err := in.Layout.RootKeyID()
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(rendered, &file); err != nil {
		t.Fatal(err)
	}
	if got := file.Services["control"].Environment[control.EnvRootKeyID]; got != want {
		t.Fatalf("control %s = %q, want %q", control.EnvRootKeyID, got, want)
	}
}
