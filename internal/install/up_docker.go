package install

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// composeArgs prefixes a Compose subcommand with the project and its file.
func (in *Installer) composeArgs(m *manifest.Manifest, args ...string) []string {
	out := []string{"compose", "-p", project(m.FactoryID), "-f", in.Layout.Compose()}
	if m.Extensions["profile"] == profileSim {
		out = append(out, "--profile", "sim")
	}
	return append(out, args...)
}

func (in *Installer) compose(ctx context.Context, m *manifest.Manifest, args ...string) error {
	_, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, args...)})
	return err
}

// bootstrapCommand is the one-shot state step run inside the project: it
// migrates the schema, creates the zone roles and stores the budget
// confirmation, if the Owner gave one.
func (in *Installer) bootstrapCommand(m *manifest.Manifest) ([]string, error) {
	cmd := []string{"sisf", "state", "bootstrap", "--dialect=postgres", "--factory-id=" + string(m.FactoryID)}
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	if err != nil || !ok {
		return cmd, err
	}
	cmd = append(cmd, "--confirm-unlimited")
	if rec.AlertOff {
		return append(cmd, "--no-alert"), nil
	}
	return append(cmd, "--alert-micros="+itoa64(int64(rec.AlertThresholdMicros))), nil
}

// upDocker builds and pins the images, renders and checks the Compose
// project, prepares the state store with its per-zone roles, and starts every
// service. The project is checked before anything runs, so the file that runs
// is the file that passed the isolation rules.
func (in *Installer) upDocker(ctx context.Context, m *manifest.Manifest) error {
	b, err := in.release(ctx, m)
	if err != nil {
		return err
	}
	bootstrap, err := in.bootstrapCommand(m)
	if err != nil {
		return err
	}
	opts := Options{
		Dir: in.Layout.Dir, FactoryID: m.FactoryID, Repo: m.Repository,
		Sim: m.Extensions["profile"] == profileSim, Debug: in.Debug,
	}
	opts.GitHubAPIURL, _ = m.Extensions["github_api_url"].(string)
	data, err := newComposeData(in.Layout, opts, b.images, bootstrap)
	if err != nil {
		return err
	}
	rendered, err := RenderCompose(data)
	if err != nil {
		return err
	}
	if vs, err := CheckCompose(in.Layout, rendered); err != nil {
		return err
	} else if len(vs) > 0 {
		return violationsError(vs)
	}
	if vs, err := CheckSecrets(in.Layout, m.Mode); err != nil {
		return err
	} else if len(vs) > 0 {
		return violationsError(vs)
	}
	if err := os.WriteFile(in.Layout.Compose(), rendered, 0o644); err != nil {
		return unavailablef(err, "cannot write %s", in.Layout.Compose())
	}
	in.say("starting the state store\n")
	if err := in.compose(ctx, m, "up", "-d", "--wait", "state"); err != nil {
		return unavailablef(err, "the state store did not become healthy")
	}
	in.say("preparing the state store and its zone roles\n")
	if err := in.compose(ctx, m, "run", "--rm", "--no-deps", "state-bootstrap"); err != nil {
		return unavailablef(err, "the state store could not be prepared")
	}
	in.say("starting the three zones\n")
	if err := in.compose(ctx, m, "up", "-d", "--wait"); err != nil {
		return unavailablef(err, "the factory did not become healthy")
	}
	in.say("factory %s is up: control http://%s, app via edge http://%s\n", m.FactoryID, addr(PortControl), addr(PortEdge))
	return nil
}

// dockerStop runs a Compose subcommand that keeps the project ("stop",
// "restart").
func (in *Installer) dockerStop(ctx context.Context, m *manifest.Manifest, verb string) error {
	if err := in.compose(ctx, m, verb); err != nil {
		return unavailablef(err, "docker compose %s failed", verb)
	}
	return nil
}

// dockerDown removes the project's containers and networks, and with
// removeData its volumes. Only this project is named, and every resource in
// it carries the project's prefix.
func (in *Installer) dockerDown(ctx context.Context, m *manifest.Manifest, removeData bool) error {
	args := []string{"down", "--remove-orphans"}
	if removeData {
		args = append(args, "--volumes")
	}
	// The bootstrap profile is enabled so a leftover one-shot container goes too.
	full := in.composeArgs(m, "--profile", "bootstrap")
	full = append(full[:len(full):len(full)], args...)
	if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: full}); err != nil {
		return unavailablef(err, "docker compose down failed")
	}
	return nil
}
