package install

import (
	"io"
	"os"
	"path/filepath"
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
// relative paths hold. The application is not one of them: the supervisor
// starts the last known good release through the app launcher and replaces
// it with each release. Output goes to a log file per unit; the returned
// closers close them.
func (in *Installer) standaloneUnits(m *manifest.Manifest) ([]unit, []io.Closer, error) {
	l := in.Layout
	common := []string{"SISF_FACTORY_ID=" + string(m.FactoryID), "SISF_CONFIG=" + l.Config()}
	ext, _ := m.Extensions["github_api_url"].(string)
	sim := m.Extensions["profile"] == profileSim

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
	edgeEnv, err := areaEnv(l.RuntimeEnv(areaEdge))
	if err != nil {
		return nil, nil, err
	}
	o := Options{Sim: sim}
	o.QAAgent, _ = m.Extensions["qa_agent"].(string)
	o.QAAgentBinary, _ = m.Extensions["qa_agent_binary"].(string)
	choice := lifecycleChoice(o, manifest.ModeStandalone)
	if err := choice.check(); err != nil {
		return nil, nil, err
	}

	bin := l.Bin
	dispatcher := []string{"dispatcher", "--admin-listen=" + addr(PortDispatcherAdmin)}
	if sim {
		dispatcher = append(dispatcher, "--agent=fake", "--fake-agent-work="+choice.AgentWork,
			"--fake-agent-patch="+filepath.Join(l.SimAgentDir(), simPatchFile))
	}
	units := []unit{
		{"supervisor", Cmd{Name: bin("sisf-supervisor"), Args: in.standaloneSupervisorArgs(m, choice),
			Env: join(supervisorEnv, "SISF_GITHUB_API_URL="+ext)}},
		{"control", Cmd{Name: bin("factoryd"), Args: []string{"control"}, Env: join(common, control,
			"SISF_LISTEN="+addr(PortControl), "SISF_GITHUB_API_URL="+ext,
			"SISF_APP_HEALTH_URL=http://"+addr(PortEdge)+"/healthz",
			"SISF_DISPATCHER_HEALTH_URL=http://"+addr(PortDispatcher)+"/healthz",
			// The edge forwards a reporter's address from loopback.
			"SISF_TRUSTED_PROXIES=127.0.0.1/32")}},
		{"dispatcher", Cmd{Name: bin("factoryd"), Args: dispatcher, Env: join(common, agent,
			"SISF_LISTEN="+addr(PortDispatcher))}},
		{"edge", Cmd{Name: bin("sisf-edge"), Args: []string{
			"--listen=" + addr(PortEdge), "--upstream=http://" + addr(PortApp), "--revision=" + string(m.Units[manifest.UnitApp].Digest),
			"--route=/webhooks/github=http://" + addr(PortControl), "--route=/report=http://" + addr(PortControl),
			"--admin-listen=" + addr(PortEdgeAdmin), "--egress-listen=",
		}, Env: edgeEnv}},
	}
	if sim {
		simEnv, err := areaEnv(l.RuntimeEnv(areaSim))
		if err != nil {
			return nil, nil, err
		}
		units = append(units, unit{"sim-github", Cmd{Name: in.Sisf, Args: []string{
			"sim", "github", "--addr=" + addr(PortFakeGitHub), "--repo=" + m.Repository.Owner + "/" + m.Repository.Repo,
			"--webhook-url=http://" + addr(PortControl) + "/webhooks/github",
		}, Env: simEnv}})
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
