package redact

import (
	"strings"
	"testing"
)

func TestShortenedIPv6AddressesAreRedacted(t *testing.T) {
	for _, addr := range []string{"2001:db8::1", "::1", "fe80::1ff:fe23:4567:890a", "2001:DB8::", "::ffff:abcd:1"} {
		for _, text := range []string{addr, "client " + addr + " failed", "[" + addr + "]:443", "from=" + addr + ", retry"} {
			got := personalData(text)
			if strings.Contains(got, addr) || !strings.Contains(got, MarkerIP) {
				t.Errorf("personalData(%q) = %q; the address is still there", text, got)
			}
		}
	}
}

func TestTextWithColonsThatIsNoAddressIsLeftAlone(t *testing.T) {
	for _, text := range []string{
		"std::string", "::", "x :: y", "12:30:45", "at 09:15", "pkg::mod::Fn()", "key::", "::main",
		"sha256:0123456789abcdef", "dead::beefcafe1", "run-1::2::3",
	} {
		if got := personalData(text); got != text {
			t.Errorf("personalData(%q) = %q; want it unchanged", text, got)
		}
	}
}
