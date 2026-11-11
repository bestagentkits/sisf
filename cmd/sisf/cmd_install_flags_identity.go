package main

import (
	"flag"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
)

// identityFlags are how people sign in to a new install. Client secrets
// are read from a file or an environment variable the flags name, never
// from a flag value, and are never printed.
type identityFlags struct {
	externalURL                   string
	owners                        repoList
	oidcIssuer, oidcClientID      string
	oidcSecretFile, oidcSecretEnv string
	ghClientID                    string
	ghSecretFile, ghSecretEnv     string
	ghURL, ghAPIURL               string
	dev                           bool
	githubCredential              string
}

func (f *identityFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.externalURL, "external-url", "", "origin people and MCP clients reach the control server at (default http://127.0.0.1:7463)")
	fs.Var(&f.owners, "owner", "sign-in identity that holds the Owner role, issuer#subject (repeatable)")
	fs.StringVar(&f.oidcIssuer, "oidc-issuer", "", "OpenID Connect issuer people sign in at (https)")
	fs.StringVar(&f.oidcClientID, "oidc-client-id", "", "OpenID Connect client ID of the factory")
	fs.StringVar(&f.oidcSecretFile, "oidc-client-secret-file", "", "file holding the OpenID Connect client secret")
	fs.StringVar(&f.oidcSecretEnv, "oidc-client-secret-env", "", "environment variable holding the OpenID Connect client secret")
	fs.StringVar(&f.ghClientID, "github-login-client-id", "", "GitHub OAuth app client ID for GitHub sign-in")
	fs.StringVar(&f.ghSecretFile, "github-login-client-secret-file", "", "file holding the GitHub OAuth app client secret")
	fs.StringVar(&f.ghSecretEnv, "github-login-client-secret-env", "", "environment variable holding the GitHub OAuth app client secret")
	fs.StringVar(&f.ghURL, "github-login-url", "", "web origin of GitHub sign-in (default https://github.com)")
	fs.StringVar(&f.ghAPIURL, "github-login-api-url", "", "REST API GitHub sign-in reads the login from (default https://api.github.com)")
	fs.BoolVar(&f.dev, "dev", false, "development install: Standalone on loopback only, signing in at the fake identity provider on 127.0.0.1:7468")
	fs.StringVar(&f.githubCredential, "github-credential", "", "with --dev: the `source` of the GitHub token; \"gh-cli\" reads it with the command gh auth token at init and at every start")
}

// identity builds the sign-in options, reading each client secret from the
// file or the variable named.
func (f identityFlags) identity(getenv func(string) string) (install.Identity, error) {
	id := install.Identity{ExternalURL: f.externalURL, Owners: f.owners, OIDCIssuer: f.oidcIssuer, OIDCClientID: f.oidcClientID,
		GitHubClientID: f.ghClientID, GitHubURL: f.ghURL, GitHubAPIURL: f.ghAPIURL}
	var err error
	if id.OIDCClientSecret, err = secretFrom("--oidc-client-secret", f.oidcSecretFile, f.oidcSecretEnv, getenv); err != nil {
		return id, err
	}
	if id.GitHubClientSecret, err = secretFrom("--github-login-client-secret", f.ghSecretFile, f.ghSecretEnv, getenv); err != nil {
		return id, err
	}
	return id, nil
}

// secretFrom reads a secret from a file or an environment variable, at
// most one of the two. The error names the flag, never the value.
func secretFrom(flag, file, env string, getenv func(string) string) (core.SecretValue, error) {
	switch {
	case file != "" && env != "":
		return core.SecretValue{}, usagef("give %s one way: -file or -env", flag)
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return core.SecretValue{}, usagef("%s-file %q cannot be read", flag, file)
		}
		value := strings.TrimSpace(string(raw))
		clear(raw)
		if value == "" {
			return core.SecretValue{}, usagef("%s-file %q is empty", flag, file)
		}
		return core.NewSecretValue([]byte(value)), nil
	case env != "":
		if !envNamePattern.MatchString(env) {
			return core.SecretValue{}, usagef("%s-env must be the name of an environment variable", flag)
		}
		value := strings.TrimSpace(getenv(env))
		if value == "" {
			return core.SecretValue{}, usagef("%s is empty or not set", env)
		}
		return core.NewSecretValue([]byte(value)), nil
	}
	return core.SecretValue{}, nil
}
