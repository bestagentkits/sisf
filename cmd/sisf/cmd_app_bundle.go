package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

type appBundleFlags struct {
	spec, repo, out, cache string
	asJSON                 bool
}

func bindAppBundleFlags(f *appBundleFlags) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(&f.spec, "spec", managedapp.FileName, "managed application Spec `file`")
		fs.StringVar(&f.repo, "repo", "", "checkout `dir` the bundle is made from; the Spec's directory when empty")
		fs.StringVar(&f.out, "out", "", "`file` to write the bundle tar to")
		fs.StringVar(&f.cache, "cache", "", "`dir` keeping verified archives by sha256; the user cache when empty")
		fs.BoolVar(&f.asJSON, "json", false, "print the result as JSON")
	}
}

func init() {
	register(Command{
		Path:    []string{"app", "bundle"},
		Args:    "--out <file> [flags]",
		Summary: "Assemble a managed application's content bundle",
		Help: `
Builds the content bundle the Spec describes: every archive its lockfile
pins is fetched, checked against its sha256 and unpacked, the prune paths
are removed and the repository's sources are copied over. Nothing in the
checkout is executed. The tar is reproducible: the same checkout always
gives the same digest, which is printed with the file and byte counts.`,
		Flags: bindAppBundleFlags(new(appBundleFlags)),
		Run:   runAppBundle,
	})
}

func runAppBundle(ctx context.Context, env *Env, args []string) error {
	var f appBundleFlags
	fs := newFlagSet("app bundle")
	bindAppBundleFlags(&f)(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if f.out == "" {
		return usagef("app bundle needs --out")
	}
	spec, err := managedapp.Load(f.spec)
	if err != nil {
		return err
	}
	if spec.Bundle.Lock == "" {
		return usagef("the Spec's bundle is a program; it is built by the factory's sandbox, not by app bundle")
	}
	repo := f.repo
	if repo == "" {
		repo = filepath.Dir(f.spec)
	}
	cache := f.cache
	if cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		cache = filepath.Join(dir, "sisf", "archives")
	}
	start := time.Now()
	res, err := appbundle.Assemble(ctx, appbundle.SpecInput(spec, repo,
		appbundle.NewHTTPFetcher(5*time.Minute, appbundle.DefaultLimits.MaxTotalBytes), cache, f.out))
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Milliseconds()
	if f.asJSON {
		return printJSON(env, map[string]any{"digest": res.Digest, "files": res.Files, "bytes": res.Bytes, "ms": elapsed, "out": f.out})
	}
	return env.Printf("%s  %d files, %d bytes, %d ms: %s\n", res.Digest, res.Files, res.Bytes, elapsed, f.out)
}
