package control_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// sendReport posts report n as if it came from remote, through a proxy that
// forwarded forwarded (empty for none).
func sendReport(e *env, n int, remote, forwarded string) int {
	body := fmt.Sprintf(`{"title":"Broken %d","body":"The page fails."}`, n)
	req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	return e.do(req).Code
}

// Users report bugs through the edge, so the route is public and limits
// each client by the address the edge forwards; a forwarded address from
// anyone but the edge is ignored, so it cannot dodge the limit.
func TestTheReportRouteIsPublicAndLimitsEachClientTheEdgeForwards(t *testing.T) {
	t.Parallel()
	if !slices.Contains(control.PublicPaths(), report.Path) {
		t.Fatalf("the report route is not a public path: %v", control.PublicPaths())
	}
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		store := report.NewMemoryStore(100)
		e.deps.Report = &control.ReportRoute{Store: store, Redactor: redact.New(redact.Config{})}
		e.restart(t)

		const edge = "127.0.0.1:40000"
		for n := range report.DefaultPerClientLimit {
			if code := sendReport(e, n, edge, "203.0.113.5"); code != http.StatusAccepted {
				t.Fatalf("report %d answered %d", n, code)
			}
		}
		if code := sendReport(e, 99, edge, "203.0.113.5"); code != http.StatusTooManyRequests {
			t.Fatalf("a client over its limit got %d", code)
		}
		if code := sendReport(e, 100, edge, "203.0.113.6"); code != http.StatusAccepted {
			t.Fatalf("another client behind the edge got %d", code)
		}

		const stranger = "198.51.100.7:5555"
		for n := range report.DefaultPerClientLimit {
			sendReport(e, 200+n, stranger, fmt.Sprintf("192.0.2.%d", n))
		}
		if code := sendReport(e, 300, stranger, "192.0.2.250"); code != http.StatusTooManyRequests {
			t.Fatalf("a forged forwarded address dodged the limit: %d", code)
		}
		if len(store.All()) != report.DefaultPerClientLimit*2+1 {
			t.Fatalf("stored %d reports", len(store.All()))
		}
	})
}

// The networks named in SISF_TRUSTED_PROXIES replace loopback: the edge
// connecting from one of them is believed, and a loopback caller is then
// limited by its own address like anyone else.
func TestTheReportRouteBelievesOnlyTheConfiguredProxies(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		e.deps.Report = &control.ReportRoute{Store: report.NewMemoryStore(100), Redactor: redact.New(redact.Config{}),
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}}
		e.restart(t)

		const edge = "172.18.0.2:40000"
		for n := range report.DefaultPerClientLimit {
			sendReport(e, n, edge, "203.0.113.5")
		}
		if code := sendReport(e, 50, edge, "203.0.113.6"); code != http.StatusAccepted {
			t.Fatalf("another client behind the configured edge got %d", code)
		}
		const local = "127.0.0.1:40001"
		for n := range report.DefaultPerClientLimit {
			sendReport(e, 100+n, local, fmt.Sprintf("192.0.2.%d", n))
		}
		if code := sendReport(e, 200, local, "192.0.2.250"); code != http.StatusTooManyRequests {
			t.Fatalf("loopback stayed trusted after the proxies were configured: %d", code)
		}
	})
}
