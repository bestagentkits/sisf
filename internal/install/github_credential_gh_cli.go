package install

import (
	"context"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// ghToken reads the token of the local gh login by running `gh auth token`.
// The token is returned wrapped and is never written anywhere but the
// secret files; a failure names the command, never its output.
func (in *Installer) ghToken(ctx context.Context) (core.SecretValue, error) {
	out, err := in.Exec.Run(ctx, Cmd{Name: "gh", Args: []string{"auth", "token"}})
	token := strings.TrimSpace(string(out))
	clear(out)
	if err != nil || token == "" || !envValuePattern.MatchString(token) {
		return core.SecretValue{}, core.NewError(core.CodeUnavailable,
			"`gh auth token` gave no usable token; log in with `gh auth login`")
	}
	return core.NewSecretValue([]byte(token)), nil
}

// refreshGHToken takes the token of the local gh login again at start and
// writes it where control and the supervisor read their GitHub token, so a
// development install follows the operator's gh login. Other installs keep
// the token they were given.
func (in *Installer) refreshGHToken(ctx context.Context, m *manifest.Manifest) error {
	if m.Extensions[extGitHubCredential] != GitHubCredentialGHCLI {
		return nil
	}
	token, err := in.ghToken(ctx)
	if err != nil {
		return err
	}
	l := in.Layout
	if err := writeSecret(l.Secret(areaControl, "github-token"), append(token.Reveal(), '\n')); err != nil {
		return err
	}
	for _, path := range []string{l.RuntimeEnv(areaControl), l.Secret(areaControl, "supervisor.env")} {
		if err := setEnvValue(path, "SISF_GITHUB_TOKEN", string(token.Reveal())); err != nil {
			return err
		}
	}
	return nil
}

// setEnvValue sets one variable of an env file this installer wrote,
// keeping the others.
func setEnvValue(path, key, value string) error {
	vars, err := readEnvFile(path)
	if err != nil {
		return err
	}
	vars[key] = value
	var pairs []string
	for _, k := range sortedKeys(vars) {
		pairs = append(pairs, k, vars[k])
	}
	data, err := formatEnv(pairs...)
	if err != nil {
		return err
	}
	defer clear(data)
	if _, err := os.Stat(path); err != nil {
		return unavailablef(err, "cannot read %s", path)
	}
	return writeSecret(path, data)
}
