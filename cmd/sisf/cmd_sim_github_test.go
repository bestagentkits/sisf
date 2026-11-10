package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func runCommand(t *testing.T, args ...string) (status int, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = commands.run(context.Background(), &Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, args)
	return status, errOut.String()
}

func TestSimGithubIsRegisteredOnTheFixedPort(t *testing.T) {
	t.Parallel()
	cmd, rest := commands.resolve([]string{"sim", "github"})
	if cmd == nil || len(rest) != 0 {
		t.Fatal("sim github is not registered")
	}
	if simGithubAddr != "127.0.0.1:7467" {
		t.Fatalf("default address = %s", simGithubAddr)
	}
}

func TestSimGithubOnlyBindsToLoopback(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"0.0.0.0:7467", ":7467", "192.168.1.5:7467", "example.com:7467", "7467", "[::]:7467"} {
		if status, stderr := runCommand(t, "sim", "github", "--addr", addr); status != exitUsage || !strings.Contains(stderr, "--addr") {
			t.Errorf("--addr %q: status %d, stderr %q; want a usage error", addr, status, stderr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if _, err := parseSimGithub([]string{"--addr", addr}); err != nil {
			t.Errorf("--addr %q rejected: %v", addr, err)
		}
	}
}

func TestSimGithubBindsToAnotherAddressOnlyWhenToldTo(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"0.0.0.0:7467", ":7467", "192.168.1.5:7467", "[::]:7467"} {
		opts, err := parseSimGithub([]string{"--addr", addr, "--allow-non-loopback"})
		if err != nil || opts.addr != addr || !opts.allowNonLoopback {
			t.Errorf("--addr %q with the opt-in: %+v %v", addr, opts, err)
		}
		if _, err := parseSimGithub([]string{"--addr", addr}); err == nil {
			t.Errorf("--addr %q was accepted without the opt-in", addr)
		}
	}
	// The opt-in widens the address, not the syntax: an address that is not
	// host:port is still a mistake.
	if _, err := parseSimGithub([]string{"--addr", "7467", "--allow-non-loopback"}); err == nil {
		t.Error("an address without a host and port was accepted")
	}
	if opts, err := parseSimGithub(nil); err != nil || opts.allowNonLoopback || opts.addr != simGithubAddr {
		t.Errorf("defaults = %+v %v, want loopback and no opt-in", opts, err)
	}
}

func TestSimGithubRejectsBadArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--repo", "nonsense"}, {"--repo", "a/b/c"}, {"--bogus"}, {"extra"}, {"--installation-id", "abc"}} {
		if status, _ := runCommand(t, append([]string{"sim", "github"}, args...)...); status != exitUsage {
			t.Errorf("%v: status %d, want %d", args, status, exitUsage)
		}
	}
}

func TestSimGithubServesTheFakeUntilInterrupted(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	opts := simGithubOptions{addr: ln.Addr().String(), repos: []string{"acme/app"}, installationID: 4242}
	go func() { done <- serveSimGithub(ctx, &Env{Stdout: &out, Stderr: io.Discard}, ln, opts, "fake-secret") }()

	base := "http://" + ln.Addr().String()
	req, _ := http.NewRequest(http.MethodPost, base+"/repos/acme/app/issues", strings.NewReader(`{"title":"hello"}`))
	req.Header.Set("Authorization", "Bearer fake-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create issue on the preconfigured repository: %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("interrupt must end the server cleanly: %v", err)
	}
	if _, err := http.Get(base + "/repos/acme/app/issues"); err == nil {
		t.Fatal("the server is still listening after it was stopped")
	}
	if !strings.Contains(out.String(), "listening on http://127.0.0.1:") {
		t.Fatalf("output = %q", out.String())
	}
}
