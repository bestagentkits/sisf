package install

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Rule names of the checks on the secrets directory.
const (
	rulePermissions = "secret.permissions"
	ruleCredentials = "secret.credentials"
	ruleZoneKeys    = "secret.zone_keys"
)

// controlOnlyKeys are the variables that exist only in the control zone's
// env file. If another zone's file has one, that zone holds a secret of
// Zone 3, which the isolation rule forbids.
var controlOnlyKeys = []string{"SISF_OWNER_TOKEN", "SISF_WEBHOOK_SECRET", "SISF_GITHUB_TOKEN", "SISF_APPROVAL_KEY",
	"SISF_ROOT_PUBLIC_KEY", "EDGE_ADMIN_TOKEN",
	"SISF_STATE_OWNER_DSN", "POSTGRES_PASSWORD"}

// allowedKeys is everything the env file of a unit outside the shared
// control runtime may hold, by path below secrets/. A key beyond these is a
// secret the unit does not need: the supervisor holds what releasing needs
// and the GitHub token, the agent zone its store role and the token of its
// own run control route, the edge only its admin token.
var allowedKeys = map[string][]string{
	"control/supervisor.env": {"SISF_ROOT_PUBLIC_KEY", "SISF_STATE_DSN", "SISF_APPROVAL_PUBLIC_KEY", "EDGE_ADMIN_TOKEN",
		"SISF_DISPATCHER_ADMIN_TOKEN", "SISF_GITHUB_TOKEN"},
	"agent/runtime.env":       {"SISF_ZONE", "SISF_STATE_DSN", "SISF_DISPATCHER_ADMIN_TOKEN"},
	"application/runtime.env": {"SISF_ZONE", "SISF_STATE_DSN"},
	"edge/runtime.env":        {"EDGE_ADMIN_TOKEN"},
}

// CheckSecrets verifies the secrets directory of an install: that only its
// owner can read it, that each zone has a credential of its own, that each
// zone opens the state store as its own role with its own password, and that
// no zone's file holds a secret that belongs to Zone 3. It is the on-disk
// half of the zone isolation guarantee.
func CheckSecrets(l Layout, mode manifest.Mode) ([]Violation, error) {
	var out []Violation
	add := func(rule, format string, args ...any) {
		out = append(out, Violation{Rule: rule, Message: sprintf(format, args...)})
	}
	root := filepath.Join(l.Dir, "secrets")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := secretFileMode
		if d.IsDir() {
			want = secretDirMode
		}
		if info.Mode().Perm() != want {
			rel, _ := filepath.Rel(l.Dir, path)
			add(rulePermissions, "%s has mode %o, want %o", rel, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		return nil, unavailablef(err, "cannot read the secrets directory")
	}

	creds := map[core.Zone]string{}
	for _, z := range core.Zones() {
		secret, err := readSecret(l.Secret(zoneArea(z), "credentials"))
		if err != nil {
			return nil, err
		}
		creds[z] = string(secret.Reveal())
	}
	for i, a := range core.Zones() {
		for _, b := range core.Zones()[i+1:] {
			if creds[a] == creds[b] {
				add(ruleCredentials, "the %s and %s zones have the same credential", a, b)
			}
		}
	}

	if qa, err := readSecret(l.Secret(areaAgent, "qa-credentials")); err != nil {
		return nil, err
	} else if string(qa.Reveal()) == creds[core.ZoneAgent] {
		add(ruleCredentials, "the QA runner has the same credential as the runner of the agent zone")
	}

	for _, z := range core.Zones() {
		env, err := readEnvFile(l.RuntimeEnv(zoneArea(z)))
		if err != nil {
			return nil, err
		}
		if z != core.ZoneControl {
			for _, key := range controlOnlyKeys {
				if _, found := env[key]; found {
					add(ruleZoneKeys, "the %s zone's env file holds %s, which belongs to the control zone", z, key)
				}
			}
		}
		if mode == manifest.ModeDocker {
			checkZoneDSN(z, env["SISF_STATE_DSN"], creds[z], add)
		}
		if z != core.ZoneControl {
			if _, err := os.Stat(l.Secret(zoneArea(z), "root-key.pem")); err == nil {
				add(ruleZoneKeys, "the %s zone's area holds the root private key", z)
			}
		}
	}
	for _, rel := range sortedKeys(allowedKeys) {
		env, err := readEnvFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		for _, key := range sortedKeys(env) {
			if !slices.Contains(allowedKeys[rel], key) {
				add(ruleZoneKeys, "%s holds %s, which its unit does not need", rel, key)
			}
		}
	}
	if sup, err := readEnvFile(l.Secret(areaControl, "supervisor.env")); err != nil {
		return nil, err
	} else if mode == manifest.ModeDocker {
		checkZoneDSN(core.ZoneControl, sup["SISF_STATE_DSN"], creds[core.ZoneControl], add)
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
