package fakeoidc

import (
	"net/http/httptest"
	"testing"
)

// NewTestServer starts a fake provider on an ephemeral loopback port and
// stops it when the test ends. Its issuer is the server's base URL.
func NewTestServer(tb testing.TB, opts ...Option) *Server {
	tb.Helper()
	ts := httptest.NewUnstartedServer(nil)
	fake, err := New("http://"+ts.Listener.Addr().String(), opts...)
	if err != nil {
		ts.Close()
		tb.Fatalf("fake provider: %v", err)
	}
	ts.Config.Handler = fake
	ts.Start()
	tb.Cleanup(ts.Close)
	return fake
}
