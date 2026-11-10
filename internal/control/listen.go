package control

import (
	"net"
	"net/netip"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DefaultListenAddr is where the server listens unless configured otherwise:
// the control port on the loopback interface only.
const DefaultListenAddr = "127.0.0.1:7463"

// validateListenAddr refuses an address that is reachable from other hosts
// unless the configuration explicitly allows it. An empty host, a wildcard
// address and any name other than localhost count as reachable.
func validateListenAddr(addr string, allowNonLoopback bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument, "the listen address must be host:port").WithCause(err)
	}
	if allowNonLoopback || host == "localhost" {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return nil
	}
	return core.NewError(core.CodePermissionDenied,
		"the control server listens on loopback only; set allow_non_loopback to expose it, and put TLS in front")
}

// validateListener applies the same rule to a listener the caller opened, so
// handing the server a wildcard socket does not bypass the configured address.
func validateListener(ln net.Listener, allowNonLoopback bool) error {
	return validateListenAddr(ln.Addr().String(), allowNonLoopback)
}
