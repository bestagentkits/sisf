package main

import (
	"context"
	"flag"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/checkrun"
)

type appCheckFlags struct {
	spec, url, only string
	writes, asJSON  bool
}

func bindAppCheckFlags(f *appCheckFlags) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(&f.spec, "spec", managedapp.FileName, "managed application Spec `file`")
		fs.StringVar(&f.url, "url", "", "base `url` of the revision to check")
		fs.StringVar(&f.only, "check", "", "run only the check of this `name`")
		fs.BoolVar(&f.writes, "writes", false, "also run checks that change data; only against a disposable copy")
		fs.BoolVar(&f.asJSON, "json", false, "print one JSON object per check")
	}
}

// checkFailed makes a failed check exit 1 after its results are printed.
type checkFailed struct{}

func (checkFailed) Error() string { return "a check failed" }
func (checkFailed) ExitCode() int { return 1 }
func (checkFailed) Silent() bool  { return true }

func init() {
	register(Command{
		Path:    []string{"app", "check"},
		Args:    "--url <url> [flags]",
		Summary: "Run a managed application's checks against a revision",
		Help: `
Runs the checks of the Spec against --url and prints, for each, whether it
passed, the slowest step's latency and, on failure, the step, the reason and
the expected and observed status. It prints nothing the application sent.
Checks that change data (a purchase) run only with --writes, which must
point at a disposable copy, never the live application. Exit status 1 when
any check fails.`,
		Flags: bindAppCheckFlags(new(appCheckFlags)),
		Run: func(ctx context.Context, env *Env, args []string) error {
			var f appCheckFlags
			fs := newFlagSet("app check")
			bindAppCheckFlags(&f)(fs)
			if err := parseFlags(fs, args); err != nil {
				return err
			}
			if f.url == "" {
				return usagef("app check needs --url")
			}
			spec, err := managedapp.Load(f.spec)
			if err != nil {
				return err
			}
			r := checkrun.Runner{BaseURL: f.url, Client: &http.Client{Timeout: 30 * time.Second}}
			failed := false
			for _, c := range spec.Checks {
				if (f.only != "" && c.Name != f.only) || (c.Writes && !f.writes) {
					continue
				}
				res := r.Run(ctx, c)
				failed = failed || !res.Passed
				if f.asJSON {
					if err := printJSON(env, res); err != nil {
						return err
					}
					continue
				}
				if res.Passed {
					err = env.Printf("pass  %-12s %5d ms (bound %d)\n", res.Check, res.LatencyMS, res.BoundMS)
				} else {
					err = env.Printf("FAIL  %-12s step %d: %s (want %d, got %d) %s\n", res.Check, res.Step, res.Reason, res.ExpectedStatus, res.ObservedStatus, res.Field)
				}
				if err != nil {
					return err
				}
			}
			if failed {
				return checkFailed{}
			}
			return nil
		},
	})
}
