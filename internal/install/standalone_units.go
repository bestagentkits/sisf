package install

import (
	"io"
	"os"
	"sort"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

const loopback = "127.0.0.1"

func addr(port int) string { return loopback + ":" + strconv.Itoa(port) }

// areaEnv reads the env file of an area as KEY=VALUE pairs in a fixed order.
func areaEnv(path string) ([]string, error) {
	vars, err := readEnvFile(path)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + vars[k]
	}
	return out, nil
}

// standaloneUnits lists the programs of a Standalone install. Each gets the
// env file of its own zone and nothing else from the secrets; they all bind
// to 127.0.0.1 and run from the install directory, where the manifest's
// relative paths hold. Output goes to a log file per unit; the returned
// closers close them.
func (in *Installer) standaloneUnits(m *manifest.Manifest) ([]unit, []io.Closer, error) {
	l := in.Layout
	common := []string{"SISF_FACTORY_ID=" + string(m.FactoryID), "SISF_CONFIG=" + l.Config()}
	ext, _ := m.Extensions["github_api_url"].(string)

	control, err := areaEnv(l.RuntimeEnv(areaControl))
	if err != nil {
		return nil, nil, err
	}
	agent, err := areaEnv(l.RuntimeEnv(areaAgent))
	if err != nil {
		return nil, nil, err
	}
	supervisorEnv, err := areaEnv(l.Secret(areaControl, "supervisor.env"))
	if err != nil {
		return nil, nil, err
	}

	bin := l.Bin
	units := []unit{
		{"supervisor", Cmd{Name: bin("sisf-supervisor"), Args: []string{
			"run", "--factory-id=" + string(m.FactoryID), "--manifest=" + l.ReleaseManifest(),
			"--root-key-ref=" + rootKeyRef, "--trust-dir=" + l.Data("trust"), "--admin=" + addr(PortSupervisor),
		}, Env: supervisorEnv}},
		{"app", Cmd{Name: bin("hello-app"), Env: []string{"HELLO_ADDR=" + addr(PortApp), "HELLO_DATA_DIR=" + l.Data("app")}}},
		{"control", Cmd{Name: bin("factoryd"), Args: []string{"control"}, Env: join(common, control,
			"SISF_LISTEN="+addr(PortControl), "SISF_GITHUB_API_URL="+ext)}},
		{"dispatcher", Cmd{Name: bin("factoryd"), Args: []string{"dispatcher"}, Env: join(common, agent,
			"SISF_LISTEN="+addr(PortDispatcher))}},
		{"edge", Cmd{Name: bin("sisf-edge"), Args: []string{
			"--listen=" + addr(PortEdge), "--upstream=http://" + addr(PortApp),
			"--route=/webhooks/github=http://" + addr(PortControl), "--route=/report=http://" + addr(PortControl),
		}}},
	}
	if m.Extensions["profile"] == profileSim {
		sim, err := areaEnv(l.RuntimeEnv(areaSim))
		if err != nil {
			return nil, nil, err
		}
		units = append(units, unit{"sim-github", Cmd{Name: in.Sisf, Args: []string{
			"sim", "github", "--addr=" + addr(PortFakeGitHub), "--repo=" + m.Repository.Owner + "/" + m.Repository.Repo,
			"--webhook-url=http://" + addr(PortControl) + "/webhooks/github",
		}, Env: sim}})
	}

	if err := os.MkdirAll(l.Logs(), 0o700); err != nil {
		return nil, nil, unavailablef(err, "cannot create the log directory")
	}
	var closers []io.Closer
	for i := range units {
		file, err := os.OpenFile(l.Logs()+"/"+units[i].name+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, secretFileMode)
		if err != nil {
			return nil, nil, unavailablef(err, "cannot open the log of %s", units[i].name)
		}
		closers = append(closers, file)
		units[i].cmd.Dir, units[i].cmd.OnlyEnv = l.Dir, true
		units[i].cmd.Stdout, units[i].cmd.Stderr = file, file
	}
	return units, closers, nil
}

// join concatenates env lists and extra assignments.
func join(lists ...any) []string {
	var out []string
	for _, l := range lists {
		switch v := l.(type) {
		case []string:
			out = append(out, v...)
		case string:
			out = append(out, v)
		}
	}
	return out
}
