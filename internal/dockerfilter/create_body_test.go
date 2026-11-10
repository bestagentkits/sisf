package dockerfilter_test

import (
	"net/http"
	"strings"
	"testing"
)

// Every create below is refused, and none reaches the daemon.
func TestUnsafeCreateBodiesAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cases := map[string]func(b map[string]any){
		"privileged":               func(b map[string]any) { host(b)["Privileged"] = true },
		"privileged lowercase key": func(b map[string]any) { delete(host(b), "Privileged"); host(b)["privileged"] = true },
		"binds":                    func(b map[string]any) { host(b)["Binds"] = []string{"/:/host"} },
		"bind mount": func(b map[string]any) {
			host(b)["Mounts"] = []any{map[string]any{"Type": "bind", "Source": "/", "Target": "/host"}}
		},
		"named volume mount": func(b map[string]any) {
			host(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "state-data", "Target": "/data"}}
		},
		"volume driver bind trick": func(b map[string]any) {
			host(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Target": "/h", "VolumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]string{"type": "none", "o": "bind", "device": "/"}}}}}
		},
		"volumes from control": func(b map[string]any) { host(b)["VolumesFrom"] = []string{"acme-app-control"} },
		"devices": func(b map[string]any) {
			host(b)["Devices"] = []any{map[string]any{"PathOnHost": "/dev/sda", "PathInContainer": "/dev/sda"}}
		},
		"device requests":           func(b map[string]any) { host(b)["DeviceRequests"] = []any{map[string]any{"Count": -1}} },
		"device cgroup rules":       func(b map[string]any) { host(b)["DeviceCgroupRules"] = []string{"a *:* rwm"} },
		"cap add":                   func(b map[string]any) { host(b)["CapAdd"] = []string{"SYS_ADMIN"} },
		"cap add lowercase key":     func(b map[string]any) { host(b)["capadd"] = []string{"SYS_ADMIN"} },
		"cap drop missing":          func(b map[string]any) { delete(host(b), "CapDrop") },
		"cap drop partial":          func(b map[string]any) { host(b)["CapDrop"] = []string{"NET_RAW"} },
		"seccomp unconfined":        func(b map[string]any) { host(b)["SecurityOpt"] = []string{"no-new-privileges", "seccomp=unconfined"} },
		"apparmor unconfined":       func(b map[string]any) { host(b)["SecurityOpt"] = []string{"apparmor=unconfined"} },
		"label disable":             func(b map[string]any) { host(b)["SecurityOpt"] = []string{"no-new-privileges", "label=disable"} },
		"no new privileges missing": func(b map[string]any) { delete(host(b), "SecurityOpt") },
		"unknown runtime":           func(b map[string]any) { host(b)["Runtime"] = "nvidia" },
		"runtime by path":           func(b map[string]any) { host(b)["Runtime"] = "/usr/bin/runc" },
		"pid host":                  func(b map[string]any) { host(b)["PidMode"] = "host" },
		"pid of control":            func(b map[string]any) { host(b)["PidMode"] = "container:acme-app-control" },
		"ipc host":                  func(b map[string]any) { host(b)["IpcMode"] = "host" },
		"uts host":                  func(b map[string]any) { host(b)["UTSMode"] = "host" },
		"userns host":               func(b map[string]any) { host(b)["UsernsMode"] = "host" },
		"cgroupns host":             func(b map[string]any) { host(b)["CgroupnsMode"] = "host" },
		"cgroup parent":             func(b map[string]any) { host(b)["CgroupParent"] = "/" },
		"network host":              func(b map[string]any) { host(b)["NetworkMode"] = "host" },
		"network bridge":            func(b map[string]any) { host(b)["NetworkMode"] = "bridge" },
		"network of control":        func(b map[string]any) { host(b)["NetworkMode"] = "container:acme-app-control" },
		"network mode missing":      func(b map[string]any) { delete(host(b), "NetworkMode") },
		"extra network": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{}, "acme-app_control": map[string]any{}}}
		},
		"only another network": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"acme-app_control": map[string]any{}}}
		},
		"alias on sandbox": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"Aliases": []string{"edge"}}}}
		},
		"fixed address on sandbox": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "172.20.0.2"}}}}
		},
		"port bindings": func(b map[string]any) {
			host(b)["PortBindings"] = map[string]any{"22/tcp": []any{map[string]any{"HostPort": "2222"}}}
		},
		"publish all ports": func(b map[string]any) { host(b)["PublishAllPorts"] = true },
		"exposed ports":     func(b map[string]any) { b["ExposedPorts"] = map[string]any{"22/tcp": map[string]any{}} },
		"anonymous volumes": func(b map[string]any) { b["Volumes"] = map[string]any{"/data": map[string]any{}} },
		"extra hosts":       func(b map[string]any) { host(b)["ExtraHosts"] = []string{"github.com:10.0.0.1"} },
		"dns":               func(b map[string]any) { host(b)["Dns"] = []string{"10.0.0.1"} },
		"log driver": func(b map[string]any) {
			host(b)["LogConfig"] = map[string]any{"Type": "syslog", "Config": map[string]string{"syslog-address": "tcp://evil:514"}}
		},
		"sysctls":                 func(b map[string]any) { host(b)["Sysctls"] = map[string]string{"net.ipv4.ip_forward": "1"} },
		"oom kill disable":        func(b map[string]any) { host(b)["OomKillDisable"] = true },
		"links":                   func(b map[string]any) { host(b)["Links"] = []string{"acme-app-control:control"} },
		"hostname":                func(b map[string]any) { b["Hostname"] = "control" },
		"healthcheck":             func(b map[string]any) { b["Healthcheck"] = map[string]any{"Test": []string{"CMD", "true"}} },
		"writable root":           func(b map[string]any) { host(b)["ReadonlyRootfs"] = false },
		"read only root missing":  func(b map[string]any) { delete(host(b), "ReadonlyRootfs") },
		"no memory limit":         func(b map[string]any) { delete(host(b), "Memory"); delete(host(b), "MemorySwap") },
		"unlimited swap":          func(b map[string]any) { host(b)["MemorySwap"] = -1 },
		"no cpu limit":            func(b map[string]any) { delete(host(b), "NanoCpus") },
		"no pids limit":           func(b map[string]any) { host(b)["PidsLimit"] = -1 },
		"tmpfs over proc":         func(b map[string]any) { host(b)["Tmpfs"] = map[string]any{"/proc/sys": ""} },
		"tmpfs with suid":         func(b map[string]any) { host(b)["Tmpfs"] = map[string]any{"/tmp": "rw,suid"} },
		"tmpfs not clean":         func(b map[string]any) { host(b)["Tmpfs"] = map[string]any{"/tmp/../etc": ""} },
		"image by tag":            func(b map[string]any) { b["Image"] = "ghcr.io/acme/sisf-runner:latest" },
		"other image by digest":   func(b map[string]any) { b["Image"] = "docker.io/library/alpine@sha256:" + strings.Repeat("b", 64) },
		"no runner label":         func(b map[string]any) { b["Labels"] = map[string]any{"sisf.run_id": "x"} },
		"runner label of another": func(b map[string]any) { b["Labels"].(map[string]any)["sisf.runner"] = "other-factory" },
		"routing label":           func(b map[string]any) { b["Labels"].(map[string]any)["traefik.enable"] = "true" },
		"root user":               func(b map[string]any) { b["User"] = "root" },
		"uid zero":                func(b map[string]any) { b["User"] = "0" },
		"uid zero padded":         func(b map[string]any) { b["User"] = "000:1000" },
		"root group":              func(b map[string]any) { b["User"] = "1000:0" },
		"user by name":            func(b map[string]any) { b["User"] = "builder" },
		"no user":                 func(b map[string]any) { delete(b, "User") },
		"no host config":          func(b map[string]any) { delete(b, "HostConfig") },
		"null host config":        func(b map[string]any) { b["HostConfig"] = nil },
		"host config lowercase":   func(b map[string]any) { b["hostconfig"] = b["HostConfig"]; delete(b, "HostConfig") },
		"privileged as text":      func(b map[string]any) { host(b)["Privileged"] = "true" },
		"memory as exponent":      func(b map[string]any) { host(b)["Memory"] = 1e30 },
		"env without name":        func(b map[string]any) { b["Env"] = []string{"=x"} },
		"relative working dir":    func(b map[string]any) { b["WorkingDir"] = "../" },
	}
	for name, mutate := range cases {
		body := validCreate()
		mutate(body)
		status, out := e.do(t, http.MethodPost, version+"/containers/create?name=sisf-runner-x", encode(t, body))
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403 (%s)", name, status, strings.TrimSpace(out))
		}
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("%d refused creates reached the daemon", len(created))
	}
}

// Bodies the standard decoder would read differently from what a quick
// look shows: repeated keys, keys in another letter case and data after
// the document.
func TestCreateBodyTricksAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	good := encode(t, validCreate())
	inner := strings.TrimSuffix(strings.TrimPrefix(good, "{"), "}")
	hostGood := encode(t, host(validCreate()))
	hostInner := strings.TrimSuffix(strings.TrimPrefix(hostGood, "{"), "}")
	cases := map[string]string{
		"duplicate privileged":         `{` + inner + `,"HostConfig":{` + hostInner + `,"Privileged":true}}`,
		"duplicate key inside host":    `{` + strings.Replace(inner, `"Privileged":false`, `"Privileged":false,"Privileged":true`, 1) + `}`,
		"host config twice by case":    `{` + inner + `,"hostConfig":{"Privileged":true}}`,
		"image twice by case":          `{` + inner + `,"IMAGE":"docker.io/library/alpine:latest"}`,
		"kelvin sign key":              `{` + strings.Replace(inner, `"NetworkMode":"`+network+`"`, `"NetworkMode":"`+network+`","NetworKMode":"host"`, 1) + `}`,
		"long s duplicate":             `{` + strings.Replace(inner, `"SecurityOpt":`, `"ſecurityOpt":["seccomp=unconfined"],"SecurityOpt":`, 1) + `}`,
		"long s alone":                 `{` + strings.Replace(inner, `"SecurityOpt":`, `"ſecurityOpt":`, 1) + `}`,
		"duplicate label":              `{` + strings.Replace(inner, `"sisf.runner":"acme-app"`, `"sisf.runner":"acme-app","sisf.runner":"other"`, 1) + `}`,
		"second document":              good + `{"HostConfig":{"Privileged":true}}`,
		"trailing garbage":             good + `x`,
		"array instead of object":      `[` + good + `]`,
		"null body":                    `null`,
		"deep nesting":                 `{"Cmd":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}`,
		"endpoints config duplicate":   `{` + strings.Replace(inner, `"EndpointsConfig":{`, `"EndpointsConfig":{"acme-app_control":{},`, 1) + `}`,
		"networking config lower case": `{` + strings.Replace(inner, `"NetworkingConfig"`, `"networkingconfig"`, 1) + `}`,
	}
	for name, body := range cases {
		status, out := e.do(t, http.MethodPost, version+"/containers/create?name=sisf-runner-x", body)
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403 (%s)", name, status, strings.TrimSpace(out))
		}
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("%d refused creates reached the daemon", len(created))
	}
}

func TestRunnerContainersNeedARunnerName(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := encode(t, validCreate())
	for _, query := range []string{"", "?name=", "?name=egress", "?name=sisf-runner-", "?name=acme-app-control",
		"?name=sisf-runner-a&name=egress", "?name=sisf-runner-a&platform=linux", "?name=sisf-runner-a/b"} {
		status, out := e.do(t, http.MethodPost, version+"/containers/create"+query, body)
		if status < 400 {
			t.Errorf("create%s: status %d (%s)", query, status, out)
		}
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("%d refused creates reached the daemon", len(created))
	}
}
