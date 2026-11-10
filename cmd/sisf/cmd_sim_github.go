package main

import (
	"context"
	"flag"
	"io"
	"net"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
)

// simGithubAddr is where the fake GitHub listens unless told otherwise.
const simGithubAddr = "127.0.0.1:7467"

// webhookSecretEnv supplies the signing secret without putting it on the
// command line, where other users of the machine could read it.
const webhookSecretEnv = "SISF_SIM_WEBHOOK_SECRET"

func init() {
	register(Command{
		Path:    []string{"sim", "github"},
		Args:    "[--addr host:port] [--repo owner/name]... [--webhook-url url] [--installation-id id]",
		Summary: "Run the fake GitHub used by the simulation profile",
		Help: `
Serves an in-memory GitHub on ` + simGithubAddr + ` until interrupted. It offers the
REST calls the factory uses for issues, labels, comments, pull requests,
check runs and releases, and sends signed webhooks to --webhook-url (the
signing secret is read from ` + webhookSecretEnv + `).

It binds to a loopback address only: it holds no real data and accepts any
bearer token, so it must not be reachable from other machines.

Control endpoints, for tests that talk to it over HTTP:
  POST /_sim/repos     {"repo":"owner/name"}
  POST /_sim/webhook   {"url":"...","secret":"..."}
  POST /_sim/faults    {"drop":n,"duplicate":n,"hold":bool,"release":"ordered|reversed"}
  GET  /app/hook/deliveries, POST /app/hook/deliveries/{id}/attempts
Send the header ` + fakegithub.ActorHeader + `: login[:ASSOCIATION] to act as a person
instead of the factory's bot.`,
		Run: runSimGithub,
	})
}

// simGithubOptions is the parsed command line of "sim github".
type simGithubOptions struct {
	addr           string
	repos          []string
	webhookURL     string
	installationID int64
}

// repoList collects repeated --repo flags.
type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error { *r = append(*r, v); return nil }

// parseSimGithub reads the flags. Mistakes are usage errors.
func parseSimGithub(args []string) (simGithubOptions, error) {
	opts := simGithubOptions{addr: simGithubAddr, installationID: fakegithub.DefaultInstallationID}
	var repos repoList
	fs := flag.NewFlagSet("sim github", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", opts.addr, "listen address (loopback only)")
	fs.Var(&repos, "repo", "repository to create, owner/name (repeatable)")
	fs.StringVar(&opts.webhookURL, "webhook-url", "", "where to send webhooks")
	fs.Int64Var(&opts.installationID, "installation-id", opts.installationID, "installation id webhooks carry")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return opts, err
		}
		return opts, usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return opts, usagef("unexpected argument %q", fs.Arg(0))
	}
	if err := requireLoopback(opts.addr); err != nil {
		return opts, err
	}
	for _, r := range repos {
		if owner, name, ok := strings.Cut(r, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return opts, usagef("repository %q must be owner/name", r)
		}
	}
	opts.repos = repos
	return opts, nil
}

// requireLoopback refuses any listen address that is not a loopback address.
// An empty host would listen on every interface.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return usagef("--addr %q must be host:port", addr)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return usagef("--addr %q is not a loopback address; the fake GitHub must not be reachable from other machines", addr)
	}
	return nil
}

func runSimGithub(ctx context.Context, env *Env, args []string) error {
	opts, err := parseSimGithub(args)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the fake GitHub cannot listen on "+opts.addr).WithCause(err)
	}
	return serveSimGithub(ctx, env, ln, opts, os.Getenv(webhookSecretEnv))
}

// serveSimGithub runs the fake on ln until ctx ends.
func serveSimGithub(ctx context.Context, env *Env, ln net.Listener, opts simGithubOptions, webhookSecret string) error {
	fake := fakegithub.New(fakegithub.WithInstallationID(opts.installationID))
	for _, repo := range opts.repos {
		if err := fake.AddRepo(repo); err != nil {
			_ = ln.Close()
			return usagef("%v", err)
		}
	}
	fake.SetWebhook(opts.webhookURL, webhookSecret)
	if err := env.Printf("fake GitHub listening on http://%s\n", ln.Addr()); err != nil {
		_ = ln.Close()
		return err
	}
	if err := fakegithub.Serve(ctx, ln, fake); err != nil {
		return core.NewError(core.CodeInternal, "the fake GitHub stopped unexpectedly").WithCause(err)
	}
	return nil
}
