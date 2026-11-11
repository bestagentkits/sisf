package main

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

func init() {
	register(Command{
		Path:    []string{"doctor"},
		Args:    "[--dir path]",
		Summary: "Check an install: manifest, signature, zones, GitHub, state, secrets, rollback",
		Help: `
Checks a factory install end to end and changes nothing:

  - factory.yaml is conformant and the unit digests are pinned;
  - the release manifest is signed by this install's root key, for this
    factory, and matches factory.yaml (Standalone: the supervisor program
    on disk is the one that was signed);
  - secrets are owner-only, each zone has a credential of its own, and
    nothing of Zone 3 sits in another zone's files;
  - Docker mode: the Compose project keeps every isolation rule (pinned
    images, loopback ports, no Docker socket or API for any service,
    per-zone credentials); it warns that container sandboxing is not
    available in this release;
  - the budget confirmation exists;
  - all three zones report healthy, the edge reaches the app, the
    dispatcher answers (Docker mode: every container is running and healthy);
  - the GitHub token reaches the repository and may write to it; the token
    is never printed;
  - the state store is open, migrated and holds the budget confirmation;
  - the supervisor passed its self-check and a last known good release is
    recorded, so a rollback has something to return to.

Each line is ok, warn or fail. A warning does not change the exit status;
any failure exits 1.`,
		Run: runDoctor,
	})
}

// failedChecks is the error of a doctor run that found failures. The report
// is already printed; this only carries the exit status.
type failedChecks struct{ count int }

func (e failedChecks) Error() string { return "the install has failing checks" }

func (e failedChecks) ExitCode() int { return exitFailure }

func runDoctor(ctx context.Context, env *Env, args []string) error {
	var dir string
	fs := newFlagSet("doctor")
	fs.StringVar(&dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	abs, err := resolveDir(dir)
	if err != nil {
		return err
	}
	in, err := newInstaller(env, abs, "", false)
	if err != nil {
		return err
	}
	report, err := in.Doctor(ctx)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the doctor was interrupted").WithCause(err)
	}
	if err := report.Write(env.Stdout); err != nil {
		return err
	}
	if _, _, fail := report.Counts(); fail > 0 {
		return failedChecks{count: fail}
	}
	return nil
}
