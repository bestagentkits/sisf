package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
)

func TestSimOIDCIsRegisteredOnTheFixedPort(t *testing.T) {
	t.Parallel()
	if cmd, rest := commands.resolve([]string{"sim", "oidc"}); cmd == nil || len(rest) != 0 {
		t.Fatal("sim oidc is not registered")
	}
	opts, err := parseSimOIDC(nil)
	if err != nil || opts.addr != "127.0.0.1:7468" || opts.allowNonLoopback || opts.clientID != simOIDCClientID || len(opts.redirects) != 2 {
		t.Fatalf("defaults = %+v %v", opts, err)
	}
}

func TestSimOIDCRejectsUnsafeOrBadArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--addr", "0.0.0.0:7468"}, {"--addr", ":7468"}, {"--addr", "7468", "--allow-non-loopback"},
		{"--issuer", "idp.example"}, {"--issuer", "ftp://idp.example"}, {"--redirect-uri", "/relative"},
		{"--client-id", ""}, {"--bogus"}, {"extra"},
	} {
		if status, _ := runCommand(t, append([]string{"sim", "oidc"}, args...)...); status != exitUsage {
			t.Errorf("%v: status %d, want %d", args, status, exitUsage)
		}
	}
	if opts, err := parseSimOIDC([]string{"--addr", "0.0.0.0:7468", "--allow-non-loopback", "--issuer", "http://fake-oidc:7468"}); err != nil || opts.issuer != "http://fake-oidc:7468" {
		t.Fatalf("opt-in address: %+v %v", opts, err)
	}
}

func TestSimOIDCServesDiscoveryUntilInterrupted(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	opts := simOIDCOptions{addr: ln.Addr().String(), clientID: "sisf-local", redirects: simOIDCRedirects}
	go func() {
		done <- serveSimOIDC(ctx, &Env{Stdout: &out, Stderr: io.Discard}, ln, opts, "sim-client-secret")
	}()

	base := "http://" + ln.Addr().String()
	resp, err := testClient.Get(base + fakeoidc.DiscoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	err = json.NewDecoder(resp.Body).Decode(&meta)
	_ = resp.Body.Close()
	if err != nil || meta["issuer"] != base || meta["jwks_uri"] != base+fakeoidc.JWKSPath {
		t.Fatalf("discovery = %v %v", meta, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("interrupt must end the server cleanly: %v", err)
	}
	if _, err := testClient.Get(base + fakeoidc.DiscoveryPath); err == nil {
		t.Fatal("the server is still listening after it was stopped")
	}
	if !strings.Contains(out.String(), "listening on "+base) || strings.Contains(out.String(), "sim-client-secret") {
		t.Fatalf("output = %q", out.String())
	}
}
