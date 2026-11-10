package main

import (
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
)

// simOIDCAddr is where the fake identity provider listens unless told otherwise.
const simOIDCAddr = "127.0.0.1:7468"

// simOIDCClientSecretEnv supplies the client secret without putting it on
// the command line. When it is empty the client is public and PKCE alone
// protects the code exchange.
const simOIDCClientSecretEnv = "SISF_SIM_OIDC_CLIENT_SECRET"

// Defaults for the relying party the factory registers as, pointing at the
// control server's login callbacks.
const (
	simOIDCClientID      = "sisf-local"
	simOIDCDefaultUser   = "owner"
	simOIDCDefaultGitHub = 1001
)

var simOIDCRedirects = []string{"http://127.0.0.1:7463/auth/callback/google", "http://127.0.0.1:7463/auth/callback/github"}

func init() {
	register(Command{
		Path:    []string{"sim", "oidc"},
		Args:    "[--addr host:port] [--allow-non-loopback] [--issuer url] [--client-id id] [--redirect-uri url]... [--sign-in-secret-file path]",
		Summary: "Run the fake identity provider used by the simulation profile",
		Help: `
Serves a fake identity provider on ` + simOIDCAddr + ` until interrupted. It speaks
Google-style OpenID Connect (discovery, JWKS, nonce, PKCE, prompt=login) and
GitHub-style OAuth (authorize, access token, /api/user and the repository
permission API). It has no login page: it signs in as the user chosen through
the control endpoints, initially "` + simOIDCDefaultUser + `" (GitHub login "` + simOIDCDefaultUser + `").

One client is registered, --client-id, with the redirect URIs given by
--redirect-uri (default: the control server's Google and GitHub callbacks).
Its secret is read from ` + simOIDCClientSecretEnv + `; when that is empty the
client is public. --issuer overrides the issuer URL, for example when other
containers reach the fake by a service name.

It binds to a loopback address only, like sim github; --allow-non-loopback
lets --addr name another address where the network around it is closed.

--sign-in-secret-file locks sign-in, as a development install does: every
sign-in and every /_sim endpoint is refused to a browser that has not first
posted the secret in that file to /_sim/unlock (a form when opened with GET).

Control endpoints, for tests that drive it over HTTP:
  POST /_sim/users        {"subject","email","email_verified","name","login","github_id"}
  POST /_sim/sign-in      {"subject":"..."}
  POST /_sim/clients      {"client_id","client_secret","redirect_uris":[...]}
  POST /_sim/faults       {"issuer","audience","nonce","lifetime","unknown_key","ignore_reauth"}
  POST /_sim/permissions  {"repo":"owner/name","login":"...","permission":"maintain"}`,
		Run: runSimOIDC,
	})
}

// simOIDCOptions is the parsed command line of "sim oidc".
type simOIDCOptions struct {
	addr             string
	issuer           string
	clientID         string
	redirects        []string
	allowNonLoopback bool
	// signInSecretFile, when set, locks sign-in behind the secret it holds.
	signInSecretFile string
	signInSecret     string
}

func parseSimOIDC(args []string) (simOIDCOptions, error) {
	opts := simOIDCOptions{addr: simOIDCAddr, clientID: simOIDCClientID}
	var redirects repoList
	fs := flag.NewFlagSet("sim oidc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", opts.addr, "listen address (loopback only)")
	fs.StringVar(&opts.issuer, "issuer", "", "issuer URL (default http://<addr>)")
	fs.StringVar(&opts.clientID, "client-id", opts.clientID, "client ID of the factory")
	fs.Var(&redirects, "redirect-uri", "registered redirect URI (repeatable)")
	fs.BoolVar(&opts.allowNonLoopback, "allow-non-loopback", false, "allow --addr to be a non-loopback address")
	fs.StringVar(&opts.signInSecretFile, "sign-in-secret-file", "", "file holding the secret a browser must post to "+simUnlockPath+" before it can sign in")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return opts, err
		}
		return opts, usagef("%v", err)
	}
	switch {
	case fs.NArg() > 0:
		return opts, usagef("unexpected argument %q", fs.Arg(0))
	case opts.clientID == "":
		return opts, usagef("--client-id must not be empty")
	}
	if opts.allowNonLoopback {
		if _, _, err := net.SplitHostPort(opts.addr); err != nil {
			return opts, usagef("--addr %q must be host:port", opts.addr)
		}
	} else if err := requireLoopback(opts.addr); err != nil {
		return opts, err
	}
	if opts.issuer != "" {
		if u, err := url.Parse(opts.issuer); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Fragment != "" {
			return opts, usagef("--issuer %q must be an absolute http(s) URL", opts.issuer)
		}
	}
	opts.redirects = simOIDCRedirects
	if len(redirects) > 0 {
		opts.redirects = redirects
	}
	for _, r := range opts.redirects {
		if u, err := url.Parse(r); err != nil || u.Scheme == "" || u.Host == "" {
			return opts, usagef("--redirect-uri %q must be an absolute URL", r)
		}
	}
	return opts, nil
}

func runSimOIDC(ctx context.Context, env *Env, args []string) error {
	opts, err := parseSimOIDC(args)
	if err != nil {
		return err
	}
	if opts.signInSecretFile != "" {
		if opts.signInSecret, err = readSignInSecret(opts.signInSecretFile); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the fake identity provider cannot listen on "+opts.addr).WithCause(err)
	}
	return serveSimOIDC(ctx, env, ln, opts, os.Getenv(simOIDCClientSecretEnv))
}

// serveSimOIDC runs the fake on ln until ctx ends.
func serveSimOIDC(ctx context.Context, env *Env, ln net.Listener, opts simOIDCOptions, clientSecret string) error {
	issuer := opts.issuer
	if issuer == "" {
		issuer = "http://" + ln.Addr().String()
	}
	fake, err := fakeoidc.New(issuer)
	if err != nil {
		_ = ln.Close()
		return err
	}
	fake.AddClient(fakeoidc.Client{ID: opts.clientID, Secret: clientSecret, RedirectURIs: opts.redirects})
	fake.AddUser(fakeoidc.User{Subject: simOIDCDefaultUser, Email: simOIDCDefaultUser + "@example.test", EmailVerified: true,
		Name: "Factory Owner", Login: simOIDCDefaultUser, GitHubID: simOIDCDefaultGitHub})
	fake.SignInAs(simOIDCDefaultUser)
	if err := env.Printf("fake identity provider listening on http://%s (issuer %s)\n", ln.Addr(), issuer); err != nil {
		_ = ln.Close()
		return err
	}
	var handler http.Handler = fake
	if opts.signInSecret != "" {
		handler = signInGate(fake, opts.signInSecret)
	}
	if err := fakegithub.Serve(ctx, ln, handler); err != nil {
		return core.NewError(core.CodeInternal, "the fake identity provider stopped unexpectedly").WithCause(err)
	}
	return nil
}
