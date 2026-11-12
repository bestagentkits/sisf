package dockerfilter_test

import (
	"bufio"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

// cliRequest is one request the docker CLI (29.8.2, API 1.47) sent for a
// command that tries to take a runner out of its sandbox. They were recorded
// on a Unix socket that answered every request with an error, so nothing was
// ever created; the image is the one the test filter allows and the network
// mode of the baseline is the test's sandbox network.
type cliRequest struct {
	Case   string `json:"case"`
	Method string `json:"method"`
	URI    string `json:"uri"`
	Body   string `json:"body"`
}

func cliRequests(t *testing.T) (baseline map[string]any, creates map[string]map[string]any, others []cliRequest) {
	t.Helper()
	f, err := os.Open("testdata/docker-cli-escape-requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	creates = map[string]map[string]any{}
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 64<<10), 64<<10)
	for lines.Scan() {
		var r cliRequest
		if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.URI, "/containers/create") {
			others = append(others, r)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
			t.Fatal(err)
		}
		if r.Case == "baseline" {
			baseline = body
			continue
		}
		creates[r.Case] = body
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if baseline == nil || len(creates) == 0 || len(others) == 0 {
		t.Fatal("the recorded requests are incomplete")
	}
	return baseline, creates, others
}

// escapeOf returns what a recorded create sets beyond the CLI's own
// baseline: the top-level and HostConfig fields whose values differ, and the
// whole NetworkingConfig when it differs.
func escapeOf(baseline, body map[string]any) (top, hostConfig map[string]any, networking any) {
	top, hostConfig = map[string]any{}, map[string]any{}
	for k, v := range body {
		if k != "HostConfig" && k != "NetworkingConfig" && !reflect.DeepEqual(v, baseline[k]) {
			top[k] = v
		}
	}
	baseHost, _ := baseline["HostConfig"].(map[string]any)
	for k, v := range body["HostConfig"].(map[string]any) {
		if !reflect.DeepEqual(v, baseHost[k]) {
			hostConfig[k] = v
		}
	}
	if !reflect.DeepEqual(body["NetworkingConfig"], baseline["NetworkingConfig"]) {
		networking = body["NetworkingConfig"]
	}
	return top, hostConfig, networking
}

// Each escape, with the exact fields and values the docker CLI sends for it,
// added to a create the filter otherwise accepts, is refused for that escape
// and never reaches the daemon.
func TestEscapesAsTheDockerCLISendsThemAreRefused(t *testing.T) {
	t.Parallel()
	baseline, creates, _ := cliRequests(t)
	reasons := map[string][]string{
		"network host":                    {`network mode "host" is not the sandbox network`},
		"network of another container":    {`network mode "container:acme-app-control" is not the sandbox network`},
		"network mode of another network": {`network mode "acme-app_control" is not the sandbox network`},
		"second network":                  {`network "acme-app_control" is not the sandbox network`, `the sandbox endpoint field`},
		"pid host":                        {`HostConfig field "PidMode" is not allowed`},
		"ipc host":                        {`HostConfig field "IpcMode" is not allowed`},
		"uts host":                        {`HostConfig field "UTSMode" is not allowed`},
		"userns host":                     {`HostConfig field "UsernsMode" is not allowed`},
		"cgroupns host":                   {`HostConfig field "CgroupnsMode" is not allowed`},
		"privileged":                      {`privileged containers are refused`},
		"cap add":                         {`adding capabilities is refused`},
		"bind of the host root":           {`HostConfig field "Binds" is not allowed`},
		"bind of the docker socket":       {`HostConfig field "Binds" is not allowed`},
		"bind mount":                      {`HostConfig field "Mounts" is not allowed`},
		"volume of another zone":          {`HostConfig field "Mounts" is not allowed`},
		"volumes from another container":  {`HostConfig field "VolumesFrom" is not allowed`},
		"device requests":                 {`HostConfig field "DeviceRequests" is not allowed`},
		"seccomp unconfined":              {`security option "seccomp=unconfined" is not allowed`},
		"published port":                  {`field "ExposedPorts" is not allowed`, `HostConfig field "PortBindings" is not allowed`},
		"alias on the sandbox":            {`the sandbox endpoint field`},
		"fixed address on the sandbox":    {`the sandbox endpoint field`},
	}
	if got := len(creates); got != len(reasons) {
		t.Fatalf("%d recorded escapes, %d expected reasons", got, len(reasons))
	}
	e := newEnv(t)
	for name, recorded := range creates {
		want, ok := reasons[name]
		if !ok {
			t.Fatalf("no expected reason for the recorded escape %q", name)
		}
		top, hostConfig, networking := escapeOf(baseline, recorded)
		if len(top)+len(hostConfig) == 0 && networking == nil {
			t.Fatalf("%s: the recording sets nothing beyond the baseline", name)
		}
		body := validCreate()
		maps.Copy(body, top)
		maps.Copy(host(body), hostConfig)
		if networking != nil {
			body["NetworkingConfig"] = networking
		}
		status, out := e.do(t, http.MethodPost, version+"/containers/create?name=sisf-runner-x", encode(t, body))
		var refusal struct{ Message string }
		_ = json.Unmarshal([]byte(out), &refusal)
		if status != http.StatusForbidden || !containsAny(refusal.Message, want) {
			t.Errorf("%s: status %d (%s), want 403 for one of %q", name, status, strings.TrimSpace(out), want)
		}
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("%d refused creates reached the daemon", len(created))
	}
}

// The CLI's bodies themselves, sent as they were recorded, are refused too:
// the filter forwards only the shape the execution backend sends.
func TestCreateBodiesOfTheDockerCLIAreRefusedAsTheyAre(t *testing.T) {
	t.Parallel()
	baseline, creates, _ := cliRequests(t)
	e := newEnv(t)
	for name, body := range creates {
		if status, out := e.do(t, http.MethodPost, version+"/containers/create?name=sisf-runner-x", encode(t, body)); status != http.StatusForbidden {
			t.Errorf("%s: status %d (%s), want 403", name, status, strings.TrimSpace(out))
		}
	}
	if status, out := e.do(t, http.MethodPost, version+"/containers/create?name=sisf-runner-x", encode(t, baseline)); status != http.StatusForbidden {
		t.Errorf("the CLI's own create on the sandbox network: status %d (%s), want 403", status, strings.TrimSpace(out))
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("%d refused creates reached the daemon", len(created))
	}
}

// Connecting a runner to another network or to the sandbox under a
// service's alias, disconnecting it, lifting its limits or restart policy
// and renaming it to a service's name are refused, with the CLI's own
// bodies, addressed to a real runner of this factory.
func TestRequestsThatWouldMoveOrChangeARunnerAreRefused(t *testing.T) {
	t.Parallel()
	_, _, others := cliRequests(t)
	e := newEnv(t)
	status, runner := e.create(t, "sisf-runner-x", validCreate())
	mustStatus(t, status, http.StatusCreated, "the runner", "")
	before := len(e.daemon.Requests())
	for _, r := range others {
		path := strings.Replace(strings.Replace(r.URI, "/v1.47/", version+"/", 1), "sbxfix-x", runner, 1)
		var body json.RawMessage
		if r.Body != "" {
			body = json.RawMessage(strings.Replace(r.Body, `"sbxfix-x"`, `"`+runner+`"`, 1))
		}
		status, out := e.do(t, r.Method, path, string(body))
		if status != http.StatusForbidden {
			t.Errorf("%s (%s %s): status %d (%s), want 403", r.Case, r.Method, path, status, strings.TrimSpace(out))
		}
	}
	if got := e.daemon.Requests()[before:]; len(got) != 0 {
		t.Fatalf("refused requests reached the daemon: %+v", got)
	}
}

func containsAny(s string, parts []string) bool {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
