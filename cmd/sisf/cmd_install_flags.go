package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// homeEnv names the directory of the install when --dir is not given.
const homeEnv = "SISF_HOME"

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// installFlags is what the install commands share. Strings are kept as typed
// so a command can tell "not given" from a default.
type installFlags struct {
	dir, mode, profile, repo, factoryID, source string
	githubTokenEnv, githubAPIURL                string
	debug                                       bool
	confirmUnlimited, noAlert                   bool
	alertThreshold                              string
}

// bindInstallFlags registers the flags of init and up.
func bindInstallFlags(fs *flag.FlagSet, f *installFlags) {
	fs.StringVar(&f.dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
	fs.StringVar(&f.mode, "mode", "", "standalone or docker (default standalone for a new install)")
	fs.StringVar(&f.profile, "profile", "", "\"sim\" runs against a fake GitHub")
	fs.StringVar(&f.repo, "repo", "", "GitHub repository to serve, owner/name")
	fs.StringVar(&f.factoryID, "factory-id", "", "factory id (default sisf-<repository name>)")
	fs.StringVar(&f.source, "source", "", "source checkout to build from (default: the current directory)")
	fs.StringVar(&f.githubTokenEnv, "github-token-env", "", "name of the environment variable holding the GitHub token")
	fs.StringVar(&f.githubAPIURL, "github-api-url", "", "GitHub API root (https, or http on loopback)")
	fs.BoolVar(&f.debug, "debug", false, "docker mode: publish the state store on 127.0.0.1:7469")
	fs.BoolVar(&f.confirmUnlimited, "confirm-unlimited", false, "confirm the unlimited budget")
	fs.StringVar(&f.alertThreshold, "alert-threshold", "", "with --confirm-unlimited: alert above this amount")
	fs.BoolVar(&f.noAlert, "no-alert", false, "with --confirm-unlimited: no budget alert")
}

// newFlagSet returns a flag set whose errors become usage errors.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses args and refuses anything left over.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return err
		}
		return usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// resolveDir returns the absolute install directory.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv(homeEnv)
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", usagef("no install directory: pass --dir or set %s", homeEnv)
		}
		dir = filepath.Join(home, ".sisf", "factory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", usagef("--dir %q is not a usable path", dir)
	}
	return abs, nil
}

// budget turns the flags into the Owner's answer; given says whether the
// Owner answered on the command line at all.
func (f installFlags) budget() (choice install.BudgetChoice, given bool, err error) {
	given = f.confirmUnlimited || f.noAlert || f.alertThreshold != ""
	if !given {
		return choice, false, nil
	}
	if !f.confirmUnlimited {
		return choice, true, usagef("--alert-threshold and --no-alert need --confirm-unlimited")
	}
	if f.noAlert == (f.alertThreshold != "") {
		return choice, true, usagef("--confirm-unlimited needs exactly one of --alert-threshold and --no-alert")
	}
	if f.noAlert {
		return install.BudgetChoice{Confirmed: true, AlertOff: true}, true, nil
	}
	choice, err = install.ParseAlert(f.alertThreshold)
	if err != nil {
		return choice, true, usagef("--alert-threshold: %v", err)
	}
	return choice, true, nil
}

// options builds the init options from the flags. Secrets are read from the
// environment here and travel no further than the installer.
func (f installFlags) options(dir string, getenv func(string) string) (install.Options, error) {
	o := install.Options{
		Dir: dir, Mode: manifest.Mode(f.mode), Sim: f.profile == "sim", FactoryID: core.FactoryID(f.factoryID),
		GitHubAPIURL: f.githubAPIURL, Debug: f.debug,
	}
	if f.profile != "" && f.profile != "sim" {
		return o, usagef("--profile must be \"sim\" or empty")
	}
	if f.repo != "" {
		owner, name, ok := strings.Cut(f.repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return o, usagef("--repo %q must be owner/name", f.repo)
		}
		o.Repo = manifest.Repository{Owner: owner, Repo: name}
	}
	switch {
	case f.githubTokenEnv != "" && !envNamePattern.MatchString(f.githubTokenEnv):
		return o, usagef("--github-token-env must be the name of an environment variable")
	case f.githubTokenEnv != "" && o.Sim:
		return o, usagef("the simulation profile generates its own token; do not pass --github-token-env")
	case f.githubTokenEnv != "":
		token := strings.TrimSpace(getenv(f.githubTokenEnv))
		if token == "" {
			return o, usagef("%s is empty or not set", f.githubTokenEnv)
		}
		o.GitHubToken = core.NewSecretValue([]byte(token))
	case !o.Sim:
		return o, usagef("a real GitHub repository needs --github-token-env NAME (or use --profile sim)")
	}
	if o.Repo == (manifest.Repository{}) && !o.Sim {
		return o, usagef("--repo owner/name is required")
	}
	return o, nil
}
