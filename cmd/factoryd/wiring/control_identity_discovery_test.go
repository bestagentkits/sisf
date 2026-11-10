package wiring

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Control waits for an identity provider that starts after it, as the
// simulation's fake does in control's own network namespace.
func TestControlWaitsForAnIdentityProviderThatStartsLater(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	issuer := "http://" + ln.Addr().String()
	_ = ln.Close() // nothing answers until the provider starts below
	cfg := auth.OIDCConfig{Name: ProviderOIDC, Issuer: issuer, ClientID: "c", RedirectURL: "http://127.0.0.1:7463/auth/callback/google",
		HTTPClient: &http.Client{Timeout: time.Second}, Clock: core.SystemClock{}}

	if _, err := discoverOIDC(context.Background(), cfg, 0, 10*time.Millisecond); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("without a wait: %v, want UNAVAILABLE at once", err)
	}
	fake, err := fakeoidc.New(issuer)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		late, err := net.Listen("tcp", ln.Addr().String())
		if err != nil {
			return // the port was taken meanwhile; the test then fails below
		}
		srv := &http.Server{Handler: fake, ReadHeaderTimeout: time.Second}
		t.Cleanup(func() { _ = srv.Close() })
		_ = srv.Serve(late)
	}()
	if _, err := discoverOIDC(context.Background(), cfg, 10*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("a provider that started later was not waited for: %v", err)
	}
}
