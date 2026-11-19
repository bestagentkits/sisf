package managedapp

import (
	"path"
	"sort"
)

// SecretsDir is where a revision reads its secrets: the Owner provisions
// each one as a file of a volume the backend mounts here, read-only. The
// factory never holds a secret's value.
const SecretsDir = "/run/secrets"

// RevisionEnv is the environment every revision of the runtime gets after
// the revision it is started as, sorted by name: the configuration, and
// NAME_FILE=SecretsDir/<secret> for each secret. It holds no secret value.
func (r Runtime) RevisionEnv() []string {
	env := make([]string, 0, len(r.Env)+len(r.Secrets))
	for name, value := range r.Env {
		env = append(env, name+"="+value)
	}
	for name, secret := range r.Secrets {
		env = append(env, name+"_FILE="+path.Join(SecretsDir, secret))
	}
	sort.Strings(env)
	return env
}
