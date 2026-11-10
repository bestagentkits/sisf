package install

import (
	"crypto/ed25519"
	"encoding/base64"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/signing"
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

// controlSecrets are the secrets only the control zone holds, and the
// tokens it shares with the one service each guards.
type controlSecrets struct {
	webhook string
	// approvalKey is the seed of the key that signs approval decisions.
	approvalKey string
	// edgeAdmin guards the edge's traffic split; the edge and the
	// supervisor hold it. edgeCounters reads the edge's counters and
	// nothing else; the edge and control hold it.
	edgeAdmin, edgeCounters string
	// oidcClientSecret and githubClientSecret are the sign-in client
	// secrets; zero when the provider has none.
	oidcClientSecret, githubClientSecret core.SecretValue
	// dispatcherAdmin guards the dispatcher's run control route, which the
	// supervisor's emergency stop calls; the dispatcher and the supervisor
	// hold it.
	dispatcherAdmin string
}

// approvalPublicKey is the public half of the approval key seed, in the
// form the supervisor verifies approval decisions with. It is what
// `factoryd approval-public-key` prints for the same seed.
func approvalPublicKey(seed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(seed)
	if err != nil || len(raw) != ed25519.SeedSize {
		return "", invalidf("the approval key is not an ed25519 seed")
	}
	priv := ed25519.NewKeyFromSeed(raw)
	clear(raw)
	pub, _ := priv.Public().(ed25519.PublicKey)
	clear(priv)
	return signing.EncodePublicKey(pub), nil
}

// runtimeEnvs builds the env file of every area. A unit gets the file of its
// own area and no other, so what a zone cannot see it cannot leak: the
// application and the agent zone hold their own store credential and nothing
// of the control zone's webhook secret, approval key, sign-in secrets,
// GitHub token or root key. The supervisor, in the control zone, holds what
// releasing needs: the GitHub token, the state store as the control role,
// the approval public key and the two admin tokens. Control reads the
// edge's counters with a token of their own and never holds the edge's
// admin token. The agent zone holds only the token its own run control
// route checks.
func (in *Installer) runtimeEnvs(o Options, pw map[core.Zone]string, cs controlSecrets, statePW string, github core.SecretValue, rootPub string) (map[string][]byte, error) {
	l := in.Layout
	dsn := func(z core.Zone) string {
		if o.Mode == manifest.ModeStandalone {
			return l.StateDB()
		}
		return postgresDSN(zoneRoles[z], pw[z])
	}
	approvalPub, err := approvalPublicKey(cs.approvalKey)
	if err != nil {
		return nil, err
	}
	control := []string{"SISF_ZONE", "control", "SISF_STATE_DSN", dsn(core.ZoneControl),
		"SISF_WEBHOOK_SECRET", cs.webhook, "SISF_APPROVAL_KEY", cs.approvalKey, wiring.EnvEdgeCountersToken, cs.edgeCounters}
	if !cs.oidcClientSecret.IsZero() {
		control = append(control, wiring.EnvOIDCClientSecret, string(cs.oidcClientSecret.Reveal()))
	}
	if !cs.githubClientSecret.IsZero() {
		control = append(control, wiring.EnvGitHubLoginClientSecret, string(cs.githubClientSecret.Reveal()))
	}
	supervisor := []string{"SISF_ROOT_PUBLIC_KEY", rootPub, "SISF_STATE_DSN", dsn(core.ZoneControl),
		"SISF_APPROVAL_PUBLIC_KEY", approvalPub, "EDGE_ADMIN_TOKEN", cs.edgeAdmin, "SISF_DISPATCHER_ADMIN_TOKEN", cs.dispatcherAdmin}
	if !github.IsZero() {
		control = append(control, "SISF_GITHUB_TOKEN", string(github.Reveal()))
		supervisor = append(supervisor, "SISF_GITHUB_TOKEN", string(github.Reveal()))
	}
	sets := map[string][]string{
		l.RuntimeEnv(areaApplication): {"SISF_ZONE", "application", "SISF_STATE_DSN", dsn(core.ZoneApplication)},
		l.RuntimeEnv(areaAgent): {"SISF_ZONE", "agent", "SISF_STATE_DSN", dsn(core.ZoneAgent),
			"SISF_DISPATCHER_ADMIN_TOKEN", cs.dispatcherAdmin},
		l.RuntimeEnv(areaControl):               control,
		l.Secret(areaControl, "supervisor.env"): supervisor,
		l.RuntimeEnv(areaEdge):                  {"EDGE_ADMIN_TOKEN", cs.edgeAdmin, wiring.EnvEdgeCountersToken, cs.edgeCounters},
	}
	if o.Mode == manifest.ModeDocker {
		sets[l.RuntimeEnv(areaState)] = []string{"POSTGRES_PASSWORD", statePW}
		sets[l.Secret(areaState, "bootstrap.env")] = []string{
			"SISF_STATE_OWNER_DSN", postgresDSN(stateOwner, statePW),
			"SISF_ZONE_PASSWORD_APPLICATION", pw[core.ZoneApplication],
			"SISF_ZONE_PASSWORD_AGENT", pw[core.ZoneAgent],
			"SISF_ZONE_PASSWORD_CONTROL", pw[core.ZoneControl],
		}
		// The one-shot trust step's file; `sisf up` fills it just before the
		// step runs and empties it after.
		sets[l.Secret(areaControl, trustEnvFile)] = []string{}
	}
	if o.Sim {
		sets[l.RuntimeEnv(areaSim)] = []string{"SISF_SIM_WEBHOOK_SECRET", cs.webhook}
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
