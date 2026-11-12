package egress

import "testing"

// The dialer's own check refuses every non-public address, whatever led
// the proxy to it.
func TestTheSocketGuardRefusesNonPublicAddresses(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:80", "100.100.100.200:80", "[fd00:ec2::254]:80", "[::ffff:10.0.0.1]:443", "0.0.0.0:443",
		"[64:ff9b::a00:1]:443", "[2002:7f00:1::]:443", "[fe80::1]:443", "224.0.0.1:443", "255.255.255.255:443", "not-an-address"} {
		if err := guardSocket("tcp", addr, nil); err == nil {
			t.Errorf("%s was let through", addr)
		}
	}
	for _, addr := range []string{"140.82.112.3:443", "[2606:4700::6810:84e5]:443"} {
		if err := guardSocket("tcp", addr, nil); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
}

func TestDisguisedNumericHostsAreRecognised(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"127.1", "2130706433", "0x7f.0.0.1", "10.0.0.1", "a.0x10.example"} {
		if !looksNumeric(host) {
			t.Errorf("%s was not recognised", host)
		}
	}
	for _, host := range []string{"github.com", "1password.com", "x1.example"} {
		if looksNumeric(host) {
			t.Errorf("%s was taken for an address", host)
		}
	}
}
