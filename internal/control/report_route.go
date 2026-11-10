package control

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// ReportRoute is the public bug report route. Users reach it through the
// edge, so every request comes from the edge's address; the client is the
// address the edge forwards, trusted only from the proxies named here.
type ReportRoute struct {
	Store    report.Store
	Redactor telemetry.Redactor
	// TrustedProxies are where the edge connects from. A forwarded address
	// from anywhere else is ignored. Empty means loopback only.
	TrustedProxies []netip.Prefix
}

// handler builds the report handler with its client key.
func (r *ReportRoute) handler(clock core.Clock) (http.Handler, error) {
	trusted := r.TrustedProxies
	if len(trusted) == 0 {
		trusted = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	}
	return report.NewHandler(report.Config{Store: r.Store, Redactor: r.Redactor, Clock: clock, ClientKey: forwardedClient(trusted)})
}

// forwardedClient names the client of a request: the last address in
// X-Forwarded-For when the request came from a trusted proxy, which sets
// that header itself and drops any value the client sent, and the remote
// address otherwise.
func forwardedClient(trusted []netip.Prefix) func(*http.Request) string {
	return func(r *http.Request) string {
		remote := remoteAddr(r)
		addr, err := netip.ParseAddr(remote)
		if err != nil || !fromAny(addr.Unmap(), trusted) {
			return remote
		}
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 0 {
			return remote
		}
		hops := strings.Split(values[len(values)-1], ",")
		client, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1]))
		if err != nil {
			return remote
		}
		return client.Unmap().String()
	}
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func fromAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
