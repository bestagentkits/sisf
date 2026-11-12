package control_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
)

// One IPv6 host is routinely given a whole /64, so a client is named by its
// /64 there: rotating addresses inside it does not make it many clients.
// An IPv4 client stays one address, also behind a trusted proxy.
func TestClientAddressGroupsAnIPv6ClientByItsSlash64(t *testing.T) {
	t.Parallel()
	direct := control.ClientAddress(nil)
	proxied := control.ClientAddress([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	key := func(name func(*http.Request) string, remote, forwarded string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		return name(r)
	}
	same := [][2]string{
		{key(direct, "[2001:db8:1:2::1]:4000", ""), key(direct, "[2001:db8:1:2:ffff:ffff:ffff:fffe]:4000", "")},
		{key(proxied, "10.0.0.1:4000", "2001:db8:1:2::1"), key(proxied, "10.0.0.1:4000", "2001:db8:1:2::abcd")},
		{key(direct, "[2001:db8:1:2::1]:4000", ""), key(proxied, "10.0.0.1:4000", "2001:db8:1:2::9")},
		{key(direct, "[::ffff:192.0.2.7]:4000", ""), key(direct, "192.0.2.7:4000", "")},
	}
	for _, pair := range same {
		if pair[0] != pair[1] {
			t.Errorf("%q and %q name one client and must have one key", pair[0], pair[1])
		}
	}
	apart := [][2]string{
		{key(direct, "[2001:db8:1:2::1]:4000", ""), key(direct, "[2001:db8:1:3::1]:4000", "")},
		{key(direct, "192.0.2.7:4000", ""), key(direct, "192.0.2.8:4000", "")},
		{key(proxied, "10.0.0.1:4000", "192.0.2.7"), key(proxied, "10.0.0.1:4000", "192.0.2.8")},
	}
	for _, pair := range apart {
		if pair[0] == pair[1] {
			t.Errorf("two clients share the key %q", pair[0])
		}
	}
	if got := key(direct, "192.0.2.7:4000", ""); got != "192.0.2.7" {
		t.Errorf("an IPv4 client is keyed %q, want its address", got)
	}
}
