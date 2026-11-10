package install

import (
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Store roles and database of Docker mode, one role per zone.
const (
	stateDatabase = "sisf"
	stateOwner    = "sisf_owner"
)

// zoneRoles are the PostgreSQL roles of the zones; they equal the store_role
// of each component in the manifest.
var zoneRoles = map[core.Zone]string{
	core.ZoneApplication: "sisf_application",
	core.ZoneAgent:       "sisf_agent",
	core.ZoneControl:     "sisf_control",
}

// postgresDSN is a connection string to the state container. TLS is off
// because the connection never leaves the project's internal networks.
func postgresDSN(user, password string) string {
	return "postgres://" + user + ":" + password + "@state:5432/" + stateDatabase + "?sslmode=disable"
}

// runtimeEnvs builds the env file of every area. A unit gets the file of its
// own area and no other, so what a zone cannot see it cannot leak: the
// application and the agent zone hold their own store credential and nothing
// of the control zone's token, webhook secret, GitHub token or root key.
func (in *Installer) runtimeEnvs(o Options, pw map[core.Zone]string, owner, webhook, statePW string, github core.SecretValue, rootPub string) (map[string][]byte, error) {
	l := in.Layout
	dsn := func(z core.Zone) string {
		if o.Mode == manifest.ModeStandalone {
			return l.StateDB()
		}
		return postgresDSN(zoneRoles[z], pw[z])
	}
	control := []string{"SISF_ZONE", "control", "SISF_STATE_DSN", dsn(core.ZoneControl),
		"SISF_OWNER_TOKEN", owner, "SISF_WEBHOOK_SECRET", webhook}
	if !github.IsZero() {
		control = append(control, "SISF_GITHUB_TOKEN", string(github.Reveal()))
	}
	sets := map[string][]string{
		l.RuntimeEnv(areaApplication):           {"SISF_ZONE", "application", "SISF_STATE_DSN", dsn(core.ZoneApplication)},
		l.RuntimeEnv(areaAgent):                 {"SISF_ZONE", "agent", "SISF_STATE_DSN", dsn(core.ZoneAgent)},
		l.RuntimeEnv(areaControl):               control,
		l.Secret(areaControl, "supervisor.env"): {"SISF_ROOT_PUBLIC_KEY", rootPub},
	}
	if o.Mode == manifest.ModeDocker {
		sets[l.RuntimeEnv(areaState)] = []string{"POSTGRES_PASSWORD", statePW}
		sets[l.Secret(areaState, "bootstrap.env")] = []string{
			"SISF_STATE_OWNER_DSN", postgresDSN(stateOwner, statePW),
			"SISF_ZONE_PASSWORD_APPLICATION", pw[core.ZoneApplication],
			"SISF_ZONE_PASSWORD_AGENT", pw[core.ZoneAgent],
			"SISF_ZONE_PASSWORD_CONTROL", pw[core.ZoneControl],
		}
	}
	if o.Sim {
		sets[l.RuntimeEnv(areaSim)] = []string{"SISF_SIM_WEBHOOK_SECRET", webhook}
	}
	out := map[string][]byte{}
	for path, pairs := range sets {
		data, err := formatEnv(pairs...)
		if err != nil {
			return nil, err
		}
		out[path] = data
	}
	return out, nil
}
