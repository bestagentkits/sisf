package main

import (
	"context"
	"flag"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
)

func init() {
	register(Command{
		Path:    []string{"doctor"},
		Args:    "[--dir path] [--json]",
		Summary: "Check an install: manifest, signature, zones, GitHub, state, secrets, rollback",
		Help: `
Checks a factory install end to end and changes nothing. First it proves
that the factory answering on the fixed ports 7460-7469 is this install's:
its sisf up process (Standalone) or Compose project (Docker) runs, and
control and the supervisor name this factory id and this install's root
key. When the factory is not running, or another factory answers there,
that is one failure with the next step and the live checks are skipped.
Then:

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

Each line is ok, warn, fail or skip. A warning or a skipped check does not
change the exit status; any failure exits 1. --json prints the same report
as one object: dir, sisf_version, ok, counts and checks (name, verdict,
detail).`,
		Flags: bindDoctorFlags(new(string), new(bool)),
		Run:   runDoctor,
	})
}

// failedChecks is the error of a doctor run that found failures. The report
// and its summary are already printed, so nothing more is said: the exit
// status carries the verdict.
type failedChecks struct{ count int }

func (e failedChecks) Error() string { return plural(e.count, "check") + " failed" }

func (e failedChecks) ExitCode() int { return exitFailure }

// Silent tells the CLI that the failure was already reported.
func (e failedChecks) Silent() bool { return true }

// plural writes a count with its noun in the right number.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// doctorJSON is the machine-readable report of --json.
type doctorJSON struct {
	Dir     string        `json:"dir"`
	Version string        `json:"sisf_version"`
	OK      bool          `json:"ok"`
	Counts  doctorCounts  `json:"counts"`
	Checks  []doctorCheck `json:"checks"`
}

type doctorCounts struct {
	OK      int `json:"ok"`
	Warn    int `json:"warn"`
	Fail    int `json:"fail"`
	Skipped int `json:"skipped"`
}

// bindDoctorFlags registers the doctor's flags into dir and asJSON.
func bindDoctorFlags(dir *string, asJSON *bool) func(*flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
		fs.BoolVar(asJSON, "json", false, "print the report as one JSON object")
	}
}

type doctorCheck struct {
	Name    string `json:"name"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
}

func runDoctor(ctx context.Context, env *Env, args []string) error {
	var dir string
	var asJSON bool
	fs := newFlagSet("doctor")
	bindDoctorFlags(&dir, &asJSON)(fs)
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
	ok, warn, fail := report.Counts()
	if asJSON {
		out := doctorJSON{Dir: abs, Version: buildVersion().Version, OK: report.OK(),
			Counts: doctorCounts{OK: ok, Warn: warn, Fail: fail, Skipped: report.Skipped()}, Checks: []doctorCheck{}}
		for _, c := range report.Checks {
			out.Checks = append(out.Checks, doctorCheck{Name: c.Name, Verdict: string(c.Verdict), Detail: c.Detail})
		}
		if err := printJSON(env, out); err != nil {
			return err
		}
	} else {
		if err := env.Printf("sisf %s, install %s\n\n", buildVersion().Version, abs); err != nil {
			return err
		}
		if err := report.Write(env.Stdout); err != nil {
			return err
		}
	}
	if fail > 0 {
		return failedChecks{count: fail}
	}
	return nil
}
