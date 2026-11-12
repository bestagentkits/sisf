package egress

import (
	"fmt"
	"net/netip"
	"syscall"
)

// nonPublic are address ranges no runner may reach, whatever the policy:
// they lead into the host, the factory's own networks or a cloud metadata
// service, or they embed such an address.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // this network
	netip.MustParsePrefix("100.64.0.0/10"),  // shared address space, also a metadata range
	netip.MustParsePrefix("192.0.0.0/24"),   // protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved and broadcast
	netip.MustParsePrefix("::/96"),          // IPv4-compatible addresses
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, embeds an IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2001::/32"),      // Teredo, embeds an IPv4 address
	netip.MustParsePrefix("2002::/16"),      // 6to4, embeds an IPv4 address
	netip.MustParsePrefix("100::/64"),       // discard
	netip.MustParsePrefix("fec0::/10"),      // deprecated site-local
	netip.MustParsePrefix("ff00::/8"),       // multicast
	netip.MustParsePrefix("169.254.0.0/16"), // link-local, including 169.254.169.254
	netip.MustParsePrefix("fd00:ec2::/32"),  // a cloud metadata range (also unique-local)
	netip.MustParsePrefix("224.0.0.0/4"),    // multicast
	netip.MustParsePrefix("255.255.255.255/32"),
}

// publicAddress reports whether a runner may connect to a.
func publicAddress(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// guardSocket is the dialer's last check: it runs on the address the socket
// is about to connect to, after every resolution, so no name and no change
// in the resolver's answer can lead a connection to a non-public address.
func guardSocket(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("egress: refusing to connect to %q: not an address", address)
	}
	if !publicAddress(ap.Addr()) {
		return fmt.Errorf("egress: refusing to connect to non-public address %s", ap.Addr())
	}
	return nil
}
