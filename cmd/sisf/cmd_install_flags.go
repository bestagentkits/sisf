package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	githubAppLogin                              string
	githubInstallationID, githubAppID           int64
	reconcileInterval                           string
	debug                                       bool
	confirmUnlimited, noAlert                   bool
	alertThreshold, budgetCap, runEstimate      string
	identity                                    identityFlags
	agents                                      agentFlags
}

// bindInstallFlags registers the flags of init and up.
func bindInstallFlags(fs *flag.FlagSet, f *installFlags) {
	fs.StringVar(&f.dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
	fs.StringVar(&f.mode, "mode", "", "standalone or docker (default standalone for a new install)")
	fs.StringVar(&f.profile, "profile", "", "\"sim\" runs against a fake GitHub")
	fs.StringVar(&f.repo, "repo", "", "GitHub repository to serve, owner/name")
	fs.StringVar(&f.factoryID, "factory-id", "", "factory id (default sisf-<repository name>; give another one to a second install of a same-named repository)")
	fs.StringVar(&f.source, "source", "", "source checkout of sisf to build from (default: the one init recorded, else the checkout holding the current directory)")
	fs.StringVar(&f.githubTokenEnv, "github-token-env", "", "name of the environment variable holding the token of the factory's GitHub account; it is read once per start and never refreshed, so use a long-lived token")
	fs.Int64Var(&f.githubInstallationID, "github-installation-id", 0, "installation of the factory's GitHub App on the repository owner, whose webhooks control accepts (required outside --profile sim and --dev with gh-cli)")
	fs.StringVar(&f.githubAppLogin, "github-app-login", "", "login of the account the factory writes as, such as my-factory[bot]; the supervisor merges only its own pull requests (required with --github-installation-id)")
	fs.Int64Var(&f.githubAppID, "github-app-id", 0, "numeric id of that account (optional)")
	fs.StringVar(&f.githubAPIURL, "github-api-url", "", "GitHub API root (https, or http on loopback)")
	fs.StringVar(&f.reconcileInterval, "github-reconcile-interval", "", "how often control asks GitHub for changed issues, 10s to 1h (default 1m)")
	fs.BoolVar(&f.debug, "debug", false, "docker mode: publish the state store on 127.0.0.1:7469")
	fs.BoolVar(&f.confirmUnlimited, "confirm-unlimited", false, "confirm the unlimited budget")
	fs.StringVar(&f.budgetCap, "budget-cap", "", "confirm a capped budget of this amount instead of the unlimited one")
	fs.StringVar(&f.runEstimate, "run-estimate", "", "with --budget-cap: amount each run sets aside before it is claimed (default a fifth of the cap)")
	fs.StringVar(&f.alertThreshold, "alert-threshold", "", "with --confirm-unlimited or --budget-cap: alert above this amount")
	fs.BoolVar(&f.noAlert, "no-alert", false, "with --confirm-unlimited: no budget alert")
	f.identity.bind(fs)
	f.agents.bind(fs)
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
		return usagef("%s", flagMessage(err))
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

// options builds the init options from the flags. Secrets are read from the
// environment here and travel no further than the installer.
func (f installFlags) options(dir string, getenv func(string) string) (install.Options, error) {
	o := install.Options{
		Dir: dir, Mode: manifest.Mode(f.mode), Sim: f.profile == "sim", FactoryID: core.FactoryID(f.factoryID),
		GitHubAPIURL: f.githubAPIURL, Debug: f.debug, Dev: f.identity.dev, GitHubCredential: f.identity.githubCredential,
		GitHubApp: install.GitHubApp{InstallationID: f.githubInstallationID, Login: f.githubAppLogin, AppID: f.githubAppID},
	}
	if f.repo == "" && f.profile != "sim" {
		return o, usagef("--repo owner/name is required: the GitHub repository the factory serves (or use --profile sim)")
	}
	if f.reconcileInterval != "" {
		d, err := time.ParseDuration(f.reconcileInterval)
		if err != nil || d <= 0 {
			return o, usagef("--github-reconcile-interval %q must be a duration such as 15s", f.reconcileInterval)
		}
		o.ReconcileInterval = d
	}
	if f.profile != "" && f.profile != "sim" {
		return o, usagef("--profile must be \"sim\" or empty")
	}
	if f.identity.dev && o.Sim {
		return o, usagef("--dev and --profile sim are different profiles; choose one")
	}
	if f.repo != "" {
		owner, name, ok := strings.Cut(f.repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return o, usagef("--repo %q must be owner/name", f.repo)
		}
		o.Repo = manifest.Repository{Owner: owner, Repo: name}
	}
	var err error
	if o.Identity, err = f.identity.identity(getenv); err != nil {
		return o, err
	}
	if err := f.agents.apply(&o); err != nil {
		return o, err
	}
	switch {
	case f.githubTokenEnv != "" && f.identity.githubCredential != "":
		return o, usagef("give the GitHub token one way: --github-token-env or --github-credential")
	case f.identity.githubCredential != "":
		// The installer runs `gh auth token` itself; the token never
		// passes through the command line.
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
		return o, usagef("a real GitHub repository needs --github-token-env NAME or, with --dev, --github-credential gh-cli (or use --profile sim)")
	}
	return o, nil
}
