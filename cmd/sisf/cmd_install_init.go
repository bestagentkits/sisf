package main

import (
	"context"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
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
	if o.Budget, err = budgetFor(env, f); err != nil {
		return err
	}
	res, err := newInstaller(env, dir, f.source, f.debug).Init(ctx, o)
	if err != nil {
		return err
	}
	return printInit(env, res)
}

// budgetFor takes the budget answer from the flags, or asks when the Owner
// is at a terminal and gave none. Without either it is left unconfirmed.
func budgetFor(env *Env, f installFlags) (install.BudgetChoice, error) {
	choice, given, err := f.budget()
	if err != nil || given || !isTerminal(env) {
		return choice, err
	}
	return install.Prompt(env.Stdin, env.Stdout)
}

// isTerminal reports whether input comes from a person at a terminal.
func isTerminal(env *Env) bool {
	file, ok := env.Stdin.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func printInit(env *Env, res install.InitResult) error {
	id := res.Options.Identity
	if err := env.Printf("factory %s created in %s\nsign in at %s as the Owner %s\n",
		res.FactoryID, res.Options.Dir, id.ExternalURL, strings.Join(id.Owners, ", ")); err != nil {
		return err
	}
	for _, w := range res.Warnings {
		if err := env.Printf("warning: %s\n", w); err != nil {
			return err
		}
	}
	return env.Printf("next: sisf up --dir %s\n", res.Options.Dir)
}
