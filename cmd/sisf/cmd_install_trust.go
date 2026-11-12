package main

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/internal/install"
)

func init() {
	register(Command{
		Path:    []string{"trust", "install"},
		Args:    "--trust-dir dir",
		Summary: "Place the supervisor's delegated keys and LKG record on its trust volume",
		Help: `
Run by "sisf up" in Docker mode, inside the Compose project, with the trust
volume mounted and nothing else. Writes the delegated keys "sisf up" issued
with the root key, and the record of the release it signed, which is the last
known good release again. The files are read from ` + install.EnvTrustFiles + `,
never from the command line, and nothing of them is printed.`,
		Run: runTrustInstall,
	})
}

func runTrustInstall(_ context.Context, env *Env, args []string) error {
	var dir string
	fs := newFlagSet("trust install")
	fs.StringVar(&dir, "trust-dir", "", "the supervisor's trust volume")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if dir == "" {
		return usagef("--trust-dir is required")
	}
	if err := install.InstallTrustFromEnv(dir, os.Getenv); err != nil {
		return err
	}
	return env.Printf("trust volume %s is ready\n", dir)
}
