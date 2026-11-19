package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

type appAdvisoriesFlags struct {
	spec, snapshot string
	asJSON         bool
}

func bindAppAdvisoriesFlags(f *appAdvisoriesFlags) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(&f.spec, "spec", managedapp.FileName, "managed application Spec `file`")
		fs.StringVar(&f.snapshot, "snapshot", "", "pinned advisory snapshot `file`")
		fs.BoolVar(&f.asJSON, "json", false, "print one JSON object per finding")
	}
}

// advisoriesFound makes findings exit 1 after they are printed.
type advisoriesFound struct{}

func (advisoriesFound) Error() string { return "the lockfile pins a vulnerable version" }
func (advisoriesFound) ExitCode() int { return 1 }
func (advisoriesFound) Silent() bool  { return true }

func init() {
	register(Command{
		Path:    []string{"app", "advisories"},
		Args:    "--snapshot <file> [flags]",
		Summary: "Report pinned archives a vulnerability advisory applies to",
		Help: `
Compares every archive the Spec's lockfile pins with a pinned advisory
snapshot (lines "kind slug first-fixed-version id severity") and prints each
one below its fixed version. A version that cannot be ordered is reported as
affected. Exit status 1 when anything is found.`,
		Flags: bindAppAdvisoriesFlags(new(appAdvisoriesFlags)),
		Run:   runAppAdvisories,
	})
}

func runAppAdvisories(_ context.Context, env *Env, args []string) error {
	var f appAdvisoriesFlags
	fs := newFlagSet("app advisories")
	bindAppAdvisoriesFlags(&f)(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if f.snapshot == "" {
		return usagef("app advisories needs --snapshot")
	}
	spec, err := managedapp.Load(f.spec)
	if err != nil {
		return err
	}
	if spec.Bundle.Lock == "" {
		return usagef("the Spec's bundle has no lockfile to check")
	}
	lockFile, err := os.Open(filepath.Join(filepath.Dir(f.spec), spec.Bundle.Lock))
	if err != nil {
		return err
	}
	defer func() { _ = lockFile.Close() }()
	lock, err := appbundle.ParseLock(lockFile)
	if err != nil {
		return err
	}
	snap, err := os.Open(f.snapshot)
	if err != nil {
		return err
	}
	defer func() { _ = snap.Close() }()
	advisories, err := appbundle.ParseAdvisories(snap)
	if err != nil {
		return err
	}
	found := appbundle.Scan(lock, advisories)
	for _, x := range found {
		if f.asJSON {
			err = printJSON(env, x)
		} else {
			err = env.Printf("%-8s %s %s %s %s (fixed in %s)\n", x.Severity, x.ID, x.Kind, x.Slug, x.Version, x.Fixed)
		}
		if err != nil {
			return err
		}
	}
	if len(found) > 0 {
		return advisoriesFound{}
	}
	return env.Printf("no advisory applies to the %d pinned archives\n", len(lock.Entries))
}
