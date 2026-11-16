package main

import (
	"context"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func runInit(ctx context.Context, env *Env, args []string) error {
	var f installFlags
	fs := newFlagSet("init")
	bindInstallFlags(fs, &f)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	dir, err := resolveDir(f.dir)
	if err != nil {
		return err
	}
	if f.mode == "" {
		f.mode = string(manifest.ModeStandalone)
	}
	o, err := f.options(dir, os.Getenv)
	if err != nil {
		return err
	}
	in, err := newInstaller(env, dir, f.source, f.debug)
	if err != nil {
		return err
	}
	// Everything init would refuse is refused before the Owner is asked
	// about money.
	if _, err := in.Preflight(o); err != nil {
		return asUsage(err)
	}
	if o.Budget, err = budgetFor(env, f); err != nil {
		return err
	}
	res, err := in.Init(ctx, o)
	if err != nil {
		return asUsage(err)
	}
	return printInit(env, res, in.SourceDir)
}

// asUsage turns an option the installer refused as INVALID_ARGUMENT into a
// usage error: a wrong invocation exits 2 with the usage line, whichever
// layer noticed it.
func asUsage(err error) error {
	if core.HasCode(err, core.CodeInvalidArgument) {
		return usagef("%s", errorMessage(err))
	}
	return err
}

// budgetFor takes the budget answer from the flags, or asks when the Owner
// is at a terminal and gave none. Without either it is left unconfirmed,
// which init then says, naming the flags that confirm it.
func budgetFor(env *Env, f installFlags) (install.BudgetChoice, error) {
	choice, given, err := f.budget()
	if err != nil || given || !isTerminal(env) {
		return choice, err
	}
	return install.Prompt(env.Stdin, env.Stdout)
}

// isTerminal reports whether input comes from a person at a terminal. A
// pipe, a file and /dev/null are not one, so scripts and CI never see a
// prompt nobody can answer.
func isTerminal(env *Env) bool {
	file, ok := env.Stdin.(*os.File)
	return ok && isTerminalFile(file)
}

// printInit reports a new install. It names the source checkout init
// recorded, found by walking up from the current directory unless --source
// named one, because every later `sisf up` builds the supervisor from it.
func printInit(env *Env, res install.InitResult, source string) error {
	id := res.Options.Identity
	if err := env.Printf("factory %s created in %s\nsign in at %s as the Owner %s\n",
		res.FactoryID, res.Options.Dir, id.ExternalURL, strings.Join(id.Owners, ", ")); err != nil {
		return err
	}
	if source != "" {
		if err := env.Printf("source checkout %s: every `sisf up` of this install builds its programs from it\n", source); err != nil {
			return err
		}
	}
	for _, w := range res.Warnings {
		if err := env.Printf("warning: %s\n", w); err != nil {
			return err
		}
	}
	return env.Printf("next: %s\n", res.Next)
}
