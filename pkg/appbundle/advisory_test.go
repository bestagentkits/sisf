package appbundle

import (
	"strings"
	"testing"
)

const snapshot = `# pinned advisory snapshot
plugin contact-form-7 5.3.2 CVE-2020-35489 critical
plugin woocommerce 5.5.1 TEST-0001 high
`

func lockOf(t *testing.T, lines ...string) Lock {
	t.Helper()
	l, err := ParseLock(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

var advSHA = strings.Repeat("a", 64)

// The vulnerable-plugin fault of the trial: Contact Form 7 5.3.1 is found,
// the fixed release is not, and an unrelated plugin is left alone.
func TestScanFindsAPinnedVulnerableVersion(t *testing.T) {
	t.Parallel()
	adv, err := ParseAdvisories(strings.NewReader(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	found := Scan(lockOf(t, "plugin contact-form-7 5.3.1 "+advSHA, "plugin woocommerce 11.2.1 "+advSHA), adv)
	if len(found) != 1 || found[0].ID != "CVE-2020-35489" || found[0].Version != "5.3.1" || found[0].Unordered {
		t.Fatalf("%+v", found)
	}
	for _, v := range []string{"5.3.2", "6.1.3"} {
		if got := Scan(lockOf(t, "plugin contact-form-7 "+v+" "+advSHA), adv); len(got) != 0 {
			t.Fatalf("%s reported: %+v", v, got)
		}
	}
}

// A version the scanner cannot order is reported, never assumed safe.
func TestScanReportsAVersionItCannotOrder(t *testing.T) {
	t.Parallel()
	adv, _ := ParseAdvisories(strings.NewReader(snapshot))
	found := Scan(lockOf(t, "plugin contact-form-7 5.3-beta "+advSHA), adv)
	if len(found) != 1 || !found[0].Unordered {
		t.Fatalf("%+v", found)
	}
}

func TestParseAdvisoriesRefusesMalformedLines(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"fields":   "plugin cf7 5.3.2 CVE-1",
		"kind":     "driver cf7 5.3.2 CVE-1 high",
		"version":  "plugin cf7 five CVE-1 high",
		"severity": "plugin cf7 5.3.2 CVE-1 urgent",
		"id":       "plugin cf7 5.3.2 $(id) high",
	} {
		if _, err := ParseAdvisories(strings.NewReader(line)); err == nil {
			t.Errorf("%s: %q accepted", name, line)
		}
	}
}
