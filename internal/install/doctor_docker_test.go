package install

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

const dockerPS = `{"Service":"state","State":"running","Health":"healthy"}
{"Service":"supervisor","State":"running","Health":"healthy"}
{"Service":"app","State":"running","Health":"healthy"}
{"Service":"control","State":"running","Health":"healthy"}
{"Service":"dispatcher","State":"running","Health":"healthy"}
{"Service":"edge","State":"running","Health":""}
`

const supervisorJSON = `{"factory_id":"sisf-hello-app","self_check":{"passed":true},"lkg":{"recorded":true}}`

// dockerFactory is a Docker install that was brought up with fakes; ps is
// what `docker compose ps` answers afterwards.
func dockerFactory(t *testing.T, ps, status string) *Installer {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	if err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	exec.answer = func(c Cmd) ([]byte, error) {
		args := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(args, " ps "):
			return []byte(ps), nil
		case strings.Contains(args, " exec -T supervisor supervisor status"):
			if status == "" {
				return nil, errors.New("no such container")
			}
			return []byte(status), nil
		}
		return nil, nil
	}
	in.HTTP = &http.Client{Transport: healthyNet().without("127.0.0.1:7465/healthz"), Timeout: time.Second}
	return in
}

func TestAHealthyDockerFactoryPassesAndSeesZonesThroughTheirContainers(t *testing.T) {
	t.Parallel()
	in := dockerFactory(t, dockerPS, supervisorJSON)
	r := runDoctor(t, in)
	for _, c := range r.Checks {
		if c.Verdict != VerdictOK && c.Name != "artifacts" && c.Name != "container sandbox" {
			t.Errorf("%s: %s %s", c.Name, c.Verdict, c.Detail)
		}
	}
	// Container sandboxing is not available yet, and the doctor says so
	// as a warning rather than claiming isolation it does not have.
	if v := verdictOf(r, "container sandbox"); len(v) != 1 || v[0] != VerdictWarn {
		t.Errorf("container sandbox = %v, want one warning", v)
	}
	for _, name := range []string{"container app", "container dispatcher", "container state", "compose isolation"} {
		if len(verdictOf(r, name)) == 0 {
			t.Errorf("no %q check ran", name)
		}
	}
	// The supervisor's own client runs inside its container; the host never
	// reaches the admin address.
	found := false
	for _, l := range in.Exec.(*fakeExec).lines() {
		found = found || strings.Contains(l, "exec -T supervisor supervisor status --admin=127.0.0.1:7464")
	}
	if !found {
		t.Error("rollback readiness was not read from the supervisor container")
	}
}

func TestAnUnhealthyOrStoppedContainerFailsTheReport(t *testing.T) {
	t.Parallel()
	ps := strings.Replace(dockerPS, `"Service":"app","State":"running","Health":"healthy"`, `"Service":"app","State":"running","Health":"unhealthy"`, 1)
	ps = strings.Replace(ps, `"Service":"dispatcher","State":"running"`, `"Service":"dispatcher","State":"exited"`, 1)
	r := runDoctor(t, dockerFactory(t, ps, supervisorJSON))
	if r.OK() {
		t.Fatal("the report passed")
	}
	for _, name := range []string{"container app", "container dispatcher"} {
		if v := verdictOf(r, name); len(v) != 1 || v[0] != VerdictFail {
			t.Errorf("%s = %v", name, v)
		}
	}
}

func TestADockerFactoryWithoutContainersSaysHowToStartIt(t *testing.T) {
	t.Parallel()
	r := runDoctor(t, dockerFactory(t, "", ""))
	if v := verdictOf(r, "containers"); len(v) != 1 || v[0] != VerdictFail || !strings.Contains(detailOf(r, "containers"), "sisf up") {
		t.Fatalf("containers = %v %q", v, detailOf(r, "containers"))
	}
	if v := verdictOf(r, "rollback"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("rollback = %v", v)
	}
}

func TestADockerProjectEditedToExposeTheSocketFailsIsolation(t *testing.T) {
	t.Parallel()
	in := dockerFactory(t, dockerPS, supervisorJSON)
	// The dispatcher is handed the raw Docker socket.
	editFile(t, in.Layout.Compose(), "  dispatcher:\n", "  dispatcher:\n    volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n")
	r := runDoctor(t, in)
	if v := verdictOf(r, "compose isolation"); len(v) == 0 || v[0] != VerdictFail {
		t.Fatalf("compose isolation = %v", v)
	}
	if r.OK() {
		t.Fatal("the report passed")
	}
}

func TestBothComposePSFormatsAreRead(t *testing.T) {
	t.Parallel()
	lines, err := parseComposePS([]byte(dockerPS))
	if err != nil || len(lines) != 6 {
		t.Fatalf("one object per line: %d %v", len(lines), err)
	}
	array := "[" + strings.ReplaceAll(strings.TrimSpace(dockerPS), "}\n{", "},{") + "]"
	arr, err := parseComposePS([]byte(array))
	if err != nil || len(arr) != 6 {
		t.Fatalf("one array: %d %v", len(arr), err)
	}
	if _, err := parseComposePS([]byte("not json")); err == nil {
		t.Fatal("garbage was accepted")
	}
}
