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

// An address written with "::" is found inside a longer run of letters,
// digits and colons: after a "label:" in front of it, before one colon that
// ends it, or both. Only the address is replaced; what surrounds it stays.
// "x2001:db8::1" reads as the label "x2001" and the address "db8::1": the
// address part is hidden, as hiding too much is the safer mistake.
func TestShortenedIPv6AddressInsideALongerRunIsRedacted(t *testing.T) {
	long := strings.Repeat("xy:", 10000)
	for text, want := range map[string]string{
		"peer 2001:db8::5: connection reset": "peer " + MarkerIP + ": connection reset",
		"lookup ::1: no such host":           "lookup " + MarkerIP + ": no such host",
		"2001:db8::1:":                       MarkerIP + ":",
		"ip:2001:db8::1":                     "ip:" + MarkerIP,
		"ip:2001:db8::1: refused":            "ip:" + MarkerIP + ": refused",
		"ip:::1":                             "ip:" + MarkerIP,
		"x2001:db8::1":                       "x2001:" + MarkerIP,
		long + "2001:db8::1:":                long + MarkerIP + ":",
	} {
		if got := personalData(text); got != want {
			t.Errorf("personalData(%q) = %q, want %q", text, got, want)
		}
	}
}

// Timestamps, MAC addresses, scoped names, ports and digests have colons
// but no address in them, also when a run of them is long.
func TestColonTextAroundWhichNoAddressStandsIsLeftAlone(t *testing.T) {
	for _, text := range []string{
		"12:30:45", "2026-11-12T12:30:45Z", "2026-11-12T12:30:45.123+07:00", "00:1a:2b:3c:4d:5e", "00:1A:2B:3C:4D:5E:",
		"std::vector", "std::vector<int>::iterator", "Foo::Bar", "Foo::Bar:", "host:8080", "host:8080:",
		"sha256:" + strings.Repeat("0123456789abcdef", 4), "label:" + strings.Repeat("xy:", 10000) + ":",
	} {
		if got := personalData(text); got != text {
			t.Errorf("personalData(%q) = %q; want it unchanged", text, got)
		}
	}
}

// An address written with "::" may be followed by a colon and a label, as
// in "fe80::1:established", or by two colons that end the run. Only the
// address is replaced. "2001:db8::1:eth0" is no address as a whole, since
// "eth0" is no group of hex digits; "2001:db8::1:beef" is one, and is
// replaced whole.
func TestShortenedIPv6AddressBeforeATrailingLabelIsRedacted(t *testing.T) {
	long := strings.Repeat("xy:", 10000)
	for text, want := range map[string]string{
		"peer fe80::1:established":  "peer " + MarkerIP + ":established",
		"x:2001:db8::1:y":           "x:" + MarkerIP + ":y",
		"host 2001:db8::1:: done":   "host " + MarkerIP + ":: done",
		"2001:db8::1:eth0":          MarkerIP + ":eth0",
		"2001:db8::1:beef":          MarkerIP,
		"ip:2001:db8::1:eth0: down": "ip:" + MarkerIP + ":eth0: down",
		long + "2001:db8::1:eth0":   long + MarkerIP + ":eth0",
		"fe80::1:eth0:up":           MarkerIP + ":eth0:up",
		"x:2001:db8::1:eth0:1500":   "x:" + MarkerIP + ":eth0:1500",
		long + "fe80::1:eth0:up:":   long + MarkerIP + ":eth0:up:",
	} {
		if got := personalData(text); got != want {
			t.Errorf("personalData(%q) = %q, want %q", text, got, want)
		}
	}
}

// Colon text with labels at its end has no address in it either.
func TestColonTextWithTrailingLabelsIsLeftAlone(t *testing.T) {
	for _, text := range []string{
		"12:30:45:eth0", "00:1a:2b:3c:4d:5e:eth0", "host:8080:tcp", "Foo::Bar:baz", "std::vector:x:y", "dead::beefcafe1:x",
	} {
		if got := personalData(text); got != text {
			t.Errorf("personalData(%q) = %q; want it unchanged", text, got)
		}
	}
}

// A run of letters, digits and colons can hold more than one address, for
// example two endpoints joined by labels. Each of them is replaced.
func TestEveryShortenedIPv6AddressOfARunIsRedacted(t *testing.T) {
	for in, want := range map[string]string{
		"src:fe80::1:dst:fe80::2":       "src:" + MarkerIP + ":dst:" + MarkerIP,
		"2001:db8::1:to:2001:db8::2":    MarkerIP + ":to:" + MarkerIP,
		"x:fe80::1:y:fe80::2:z:fe80::3": "x:" + MarkerIP + ":y:" + MarkerIP + ":z:" + MarkerIP,
	} {
		if got := personalData(in); got != want {
			t.Errorf("personalData(%q) = %q, want %q", in, got, want)
		}
	}
}
