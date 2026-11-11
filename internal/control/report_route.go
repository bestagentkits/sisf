package control

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// ReportRoute is the public bug report route. Users reach it through the
// edge, so every request comes from the edge's address; the client is the
// address the edge forwards, trusted only from the proxies named here.
type ReportRoute struct {
	// Store keeps the reports. Nil keeps them in the control zone's
	// operational store (Deps.Store), so a restart loses none.
	Store    report.Store
	Redactor telemetry.Redactor
	// MaxStored and Retention bound the reports kept in the operational
	// store: a full store removes a report of the oldest day stored to make
	// room for a new one, and a report is removed once its retention has
	// passed since it was first received, by the next save or the next
	// Server.PruneReports, whichever comes first. Zero means
	// DefaultMaxStoredReports and the report retention of
	// redact.DefaultRetention.
	MaxStored int
	Retention time.Duration
	// TrustedProxies are where the edge connects from. A forwarded address
	// from anywhere else is ignored. Empty means loopback only.
	TrustedProxies []netip.Prefix
}

// handler builds the report handler with its client key, keeping reports
// in operational when the route names no store of its own; kept is that
// store, nil when the route names its own.
func (r *ReportRoute) handler(clock core.Clock, operational state.Store) (h http.Handler, kept *storedReports, err error) {
	store := r.Store
	if store == nil {
		if r.MaxStored < 0 || r.Retention < 0 {
			return nil, nil, core.NewError(core.CodeInvalidArgument, "report limits must not be negative")
		}
		kept = &storedReports{store: operational, capacity: r.MaxStored, retention: r.Retention}
		if kept.capacity == 0 {
			kept.capacity = DefaultMaxStoredReports
		}
		if kept.retention == 0 {
			kept.retention = redact.DefaultRetention()[redact.KindReport]
		}
		store = *kept
	}
	h, err = report.NewHandler(report.Config{Store: store, Redactor: r.Redactor, Clock: clock, ClientKey: ClientAddress(r.TrustedProxies)})
	return h, kept, err
}

// ClientAddress returns what names the client of a request behind the
// proxies in trusted (empty means loopback): the last address in
// X-Forwarded-For when the request came from a trusted proxy, which sets
// that header itself and drops any value the client sent, and the remote
// address otherwise. Limits per client, such as those of the report route
// and of sign-in, key on it. An IPv6 client is named by its /64, the block
// one host is routinely given, so rotating addresses inside it does not
// make it many clients; an IPv4 client is named by its address.
func ClientAddress(trusted []netip.Prefix) func(*http.Request) string {
	if len(trusted) == 0 {
		trusted = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	}
	return func(r *http.Request) string {
		remote := remoteAddr(r)
		addr, err := netip.ParseAddr(remote)
		if err != nil {
			return remote
		}
		if !fromAny(addr.Unmap(), trusted) {
			return clientOf(addr)
		}
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 0 {
			return clientOf(addr)
		}
		hops := strings.Split(values[len(values)-1], ",")
		client, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1]))
		if err != nil {
			return clientOf(addr)
		}
		return clientOf(client)
	}
}

// clientOf names the client at addr: its /64 for IPv6, its address for
// IPv4 (an IPv4-mapped address included).
func clientOf(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	if !addr.Is6() {
		return addr.String()
	}
	block, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return block.String()
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
