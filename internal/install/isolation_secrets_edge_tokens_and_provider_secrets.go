package install

import (
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/core"
)

// edgeTokenProblems checks the edge's two tokens: the edge and control hold
// the same counters token, and it is not the admin token, which the edge
// refuses to start with.
func edgeTokenProblems(l Layout) ([]string, error) {
	edge, err := readEnvFile(l.RuntimeEnv(areaEdge))
	if err != nil {
		return nil, err
	}
	control, err := readEnvFile(l.RuntimeEnv(areaControl))
	if err != nil {
		return nil, err
	}
	counters, admin := edge[wiring.EnvEdgeCountersToken], edge["EDGE_ADMIN_TOKEN"]
	var out []string
	switch {
	case len(counters) < 32 || len(admin) < 32:
		out = append(out, "the edge lacks its admin token or its read-only counters token")
	case counters == admin:
		out = append(out, "the edge's counters token is its admin token; they must differ, or the edge refuses to start")
	}
	if control[wiring.EnvEdgeCountersToken] != counters {
		out = append(out, "control's counters token is not the edge's, so the decision layer cannot read the counters")
	}
	return out, nil
}

// checkZoneDSN requires a zone's connection string to name the zone's own
// role and password, so no zone can open the store as another.
func checkZoneDSN(z core.Zone, dsn, credential string, add func(rule, format string, args ...any)) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		add(ruleCredentials, "the %s zone has no store connection of its own", z)
		return
	}
	password, _ := u.User.Password()
	if u.User.Username() != zoneRoles[z] {
		add(ruleCredentials, "the %s zone opens the store as role %q, want %q", z, u.User.Username(), zoneRoles[z])
	}
	if password != credential {
		add(ruleCredentials, "the %s zone's store password is not the zone's own credential", z)
	}
	if u.Path != "/"+stateDatabase {
		add(ruleCredentials, "the %s zone's store connection names another database", z)
	}
}

// controlProviderSecret reports whether key is a secret the operator gave
// the control zone's env provider: a SISF_CONTROL_SECRET_ variable of the
// control process's env file, and of no other file.
func controlProviderSecret(rel, key string) bool {
	return rel == "control/runtime.env" && strings.HasPrefix(key, wiring.EnvControlSecretPrefix) &&
		len(key) > len(wiring.EnvControlSecretPrefix)
}
