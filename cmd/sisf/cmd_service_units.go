package main

import (
	"context"
	"flag"
)

func init() {
	register(Command{
		Path:    []string{"service", "units"},
		Args:    "[--dir path] [--source path]",
		Summary: "Write the systemd and launchd units of a Standalone install again",
		Help: `
Writes the service units of an existing Standalone install into its service/
directory, for when init could not write them (sisf ran from a temporary
` + "`go run`" + ` build, or go was not on PATH) or after sisf or the source checkout
moved. The units run this sisf binary, so run it from where it stays installed.
Nothing else in the install changes.`,
		Flags: bindServiceUnitsFlags(new(string), new(string)),
		Run: func(_ context.Context, env *Env, args []string) error {
			var dir, source string
			fs := newFlagSet("service units")
			bindServiceUnitsFlags(&dir, &source)(fs)
			if err := parseFlags(fs, args); err != nil {
				return err
			}
			abs, err := resolveDir(dir)
			if err != nil {
				return err
			}
			in, err := newInstaller(env, abs, source, false)
			if err != nil {
				return err
			}
			if err := in.WriteServiceUnits(); err != nil {
				return asUsage(err)
			}
			return env.Printf("service units written to %s\n", in.Layout.ServiceDir())
		},
	})
}

func bindServiceUnitsFlags(dir, source *string) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
		fs.StringVar(source, "source", "", "source checkout of sisf (default the one the install recorded)")
	}
}
