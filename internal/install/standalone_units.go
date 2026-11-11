package install

import (
	"cmp"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
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
	o, choice, err := in.standaloneChoice(m)
	if err != nil {
		return nil, nil, err
	}

	bin := l.Bin
	units := []unit{
		{"supervisor", Cmd{Name: bin("sisf-supervisor"), Args: in.standaloneSupervisorArgs(m, choice),
			Env: join(supervisorEnv, "SISF_GITHUB_API_URL="+ext)}},
		{"control", Cmd{Name: bin("factoryd"), Args: []string{"control"}, Env: join(common, control, in.standaloneControlEnv(o),
			"SISF_LISTEN="+addr(PortControl), "SISF_GITHUB_API_URL="+ext,
			"SISF_APP_HEALTH_URL=http://"+addr(PortEdge)+"/healthz",
			"SISF_DISPATCHER_HEALTH_URL=http://"+addr(PortDispatcher)+"/healthz",
			// The edge forwards a reporter's address from loopback.
			"SISF_TRUSTED_PROXIES=127.0.0.1/32")}},
		{"dispatcher", Cmd{Name: bin("factoryd"), Args: in.standaloneDispatcherArgs(choice, sim), Env: join(common, agent,
			"SISF_LISTEN="+addr(PortDispatcher), operatorAccountEnv(choice.Agents.AgentHome, os.Getenv))}},
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
	if o.Identity.usesFake() {
		// The fake identity provider's client is public. It starts first:
		// control discovers the provider when it starts.
		args := []string{"sim", "oidc", "--addr=" + addr(PortFakeOIDC)}
		if !sim {
			// Outside the simulation real agents may run on this machine
			// and reach every loopback port, so sign-in is locked.
			path, err := in.writeDevSignInSecret()
			if err != nil {
				return nil, nil, err
			}
			args = append(args, "--sign-in-secret-file="+path)
		}
		units = append([]unit{{"sim-oidc", Cmd{Name: in.Sisf, Args: args}}}, units...)
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

// standaloneControlEnv is the control process's sign-in contract, its way
// to the supervisor's admin socket and the edge's counters, and the docs
// tree of the source checkout when there is one.
func (in *Installer) standaloneControlEnv(o Options) []string {
	env := append(o.Identity.controlEnv(),
		wiring.EnvSupervisorAdminAddr+"="+addr(PortSupervisor),
		wiring.EnvEdgeCountersURL+"=http://"+addr(PortEdgeAdmin),
		wiring.EnvLocalStoreDir+"="+in.Layout.Data("control", "secrets"))
	env = append(env, o.reconcileEnv()...)
	docs := filepath.Join(in.SourceDir, "docs")
	if info, err := os.Stat(docs); err == nil && info.IsDir() && quotedPathPattern.MatchString(docs) {
		env = append(env, wiring.EnvDocsDir+"="+docs)
	}
	return env
}

// standaloneDispatcherArgs is the dispatcher's command line: the scripted
// agent, which leaves the install's scripted patch, or the real coding
// agent with its workspaces, and the amount each run sets aside from a
// capped budget.
func (in *Installer) standaloneDispatcherArgs(c LifecycleChoice, sim bool) []string {
	l := in.Layout
	args := []string{"dispatcher", "--admin-listen=" + addr(PortDispatcherAdmin)}
	switch {
	case sim || !c.Agents.real():
		args = append(args, "--agent=fake", "--fake-agent-work="+cmp.Or(c.AgentWork, simAgentWork), "--fake-agent-patch="+filepath.Join(l.SimAgentDir(), simPatchFile))
	default:
		a := c.Agents
		args = append(args, "--agent="+a.Coder, "--workspace-root="+l.Data("workspaces"),
			"--workspace-source="+a.WorkspaceSource, "--artifact-dir="+l.Data("artifacts"))
		if a.AgentHome != "" {
			args = append(args, "--agent-home="+a.AgentHome)
		}
		if a.CoderModel != "" {
			args = append(args, "--agent-model="+a.CoderModel)
		}
	}
	if c.RunEstimateMicros > 0 {
		args = append(args, "--run-estimate-micros="+itoa64(c.RunEstimateMicros))
	}
	return args
}

// operatorAccountEnv is the operator's USER for a dispatcher whose coding
// agent runs with the operator's own home. The units start with a minimal
// environment, and an agent that keeps its login in the operating system's
// keychain finds it only under that account name; the dispatcher hands the
// name on to such an agent and to no other.
func operatorAccountEnv(agentHome string, getenv func(string) string) []string {
	if user := getenv("USER"); agentHome != "" && user != "" {
		return []string{"USER=" + user}
	}
	return nil
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

// writeDevSignInSecret writes a new secret that unlocks sign-in at the fake
// identity provider of a development install, for this start of `sisf up`,
// and tells the operator where it is. It is never put in an environment, a
// command line or a log; the file is the operator's only, in the control
// secrets of the install directory, which lies apart from every agent's
// home and workspace.
func (in *Installer) writeDevSignInSecret() (string, error) {
	secret, err := newToken(in.Rand)
	if err != nil {
		return "", err
	}
	path := in.Layout.DevSignInSecret()
	if err := writeSecret(path, []byte(secret+"\n")); err != nil {
		return "", err
	}
	in.say("sign-in at the development identity provider is locked: open http://%s/_sim/unlock and enter the secret in %s\n",
		addr(PortFakeOIDC), path)
	return path, nil
}

// standaloneChoice is the options and the lifecycle of a Standalone install,
// as its units are started with them.
func (in *Installer) standaloneChoice(m *manifest.Manifest) (Options, LifecycleChoice, error) {
	o, err := in.installedOptions(m)
	if err != nil {
		return o, LifecycleChoice{}, err
	}
	choice := lifecycleChoice(o, manifest.ModeStandalone)
	return o, choice, choice.check()
}

// StandaloneSupervisorArgs is the supervisor's command line, after the
// program name, of the Standalone install m describes, as `sisf up` starts
// it. It writes nothing.
func (in *Installer) StandaloneSupervisorArgs(m *manifest.Manifest) ([]string, error) {
	_, choice, err := in.standaloneChoice(m)
	if err != nil {
		return nil, err
	}
	return in.standaloneSupervisorArgs(m, choice), nil
}
