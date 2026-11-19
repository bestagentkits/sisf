package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/detect"
)

// adoptFlags are the settings of sisf adopt.
type adoptFlags struct {
	name, out, root string
	port            int
	env, data       multiFlag
	stdout, force   bool
}

// multiFlag collects a repeatable flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func bindAdoptFlags(f *adoptFlags) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(&f.name, "name", "", "the application's `name`; the directory's name when empty")
		fs.StringVar(&f.out, "out", "", "`file` to write the Spec to; <dir>/"+managedapp.FileName+" when empty")
		fs.StringVar(&f.root, "root", "", "repository root `dir` the application is in; <dir> when empty")
		fs.IntVar(&f.port, "port", 0, "`port` the application listens on, for a stack that does not publish one")
		fs.Var(&f.env, "env", "configuration `NAME=value` that is not secret; repeatable")
		fs.Var(&f.data, "data", "named volume `name:/target` the application writes to; repeatable")
		fs.BoolVar(&f.stdout, "stdout", false, "print the Spec instead of writing it")
		fs.BoolVar(&f.force, "force", false, "replace an existing Spec")
	}
}

var adoptName = regexp.MustCompile(`[^a-z0-9-]+`)

func init() {
	register(Command{
		Path:    []string{"adopt"},
		Args:    "[flags] <dir>",
		Summary: "Describe an existing application as a managed application Spec",
		Help: `
Reads the application in <dir> (a Compose project, or a Go program) and
writes ` + managedapp.FileName + `: the runtime image every revision runs in,
how its content bundle is made, its data volumes and dependencies, the
checks its behaviour is judged by, and which of its changes only the Owner
may approve. Review the notes it prints, then commit the file: the factory
reads it from the Owner-approved branch, never from a candidate.

Adoption never copies a secret value. A secret is a Compose secret
(NAME_FILE=/run/secrets/name); a password in plain environment is refused.`,
		Flags: bindAdoptFlags(new(adoptFlags)),
		Run:   runAdopt,
	})
}

func runAdopt(_ context.Context, env *Env, args []string) error {
	var f adoptFlags
	fs := newFlagSet("adopt")
	bindAdoptFlags(&f)(fs)
	// The directory may come before or after the flags.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return err
			}
			return usagef("%s", flagMessage(err))
		}
		if fs.NArg() == 0 {
			break
		}
		positional, args = append(positional, fs.Arg(0)), fs.Args()[1:]
	}
	if len(positional) != 1 {
		return usagef("adopt needs one directory")
	}
	dir, err := filepath.Abs(positional[0])
	if err != nil {
		return err
	}
	opts, err := f.options(dir)
	if err != nil {
		return err
	}
	name := f.name
	if name == "" {
		name = strings.Trim(adoptName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	}
	res, err := detect.Adopt(dir, name, opts)
	if err != nil {
		return err
	}
	data, err := managedapp.Marshal(res.Spec, "Written by sisf adopt; review it, then commit it.\nThe factory reads this file from the Owner-approved branch only.")
	if err != nil {
		return err
	}
	if f.stdout {
		_, err := env.Stdout.Write(data)
		return err
	}
	out := f.out
	if out == "" {
		out = filepath.Join(dir, managedapp.FileName)
	}
	if _, err := os.Stat(out); err == nil && !f.force {
		return usagef("%s exists; pass --force to replace it", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	if err := env.Printf("adopted %s as a %s application: %s\n", name, res.Spec.Stack, out); err != nil {
		return err
	}
	for _, n := range res.Notes {
		if err := env.Printf("  review: %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

// options turns the flags into adoption options.
func (f adoptFlags) options(dir string) (detect.Options, error) {
	opts := detect.Options{Root: f.root, Port: f.port, Env: map[string]string{}}
	if opts.Root != "" {
		root, err := filepath.Abs(opts.Root)
		if err != nil {
			return opts, err
		}
		opts.Root = root
	}
	if f.port < 0 || f.port > 65535 {
		return opts, usagef("--port %s is not a port", strconv.Itoa(f.port))
	}
	for _, kv := range f.env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return opts, usagef("--env %q is not NAME=value", k)
		}
		if managedapp.SecretLike(k) && !managedapp.Flag(v) {
			return opts, usagef("--env %s looks like a secret; give it as a secret, never on a command line", k)
		}
		opts.Env[k] = v
	}
	for _, d := range f.data {
		n, t, ok := strings.Cut(d, ":")
		if !ok || n == "" || t == "" {
			return opts, usagef("--data %q is not name:/target", d)
		}
		opts.Data = append(opts.Data, managedapp.Volume{Name: n, Target: t})
	}
	return opts, nil
}
