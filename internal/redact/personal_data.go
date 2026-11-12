package redact

import (
	"net/netip"
	"regexp"
	"strings"
)

// personalRule finds one kind of personal data. valid, when set, confirms a
// candidate match, so numbers that merely look alike are left alone.
// rewrite, when set instead, returns the candidate match with the personal
// data in it replaced by marker and the rest as it was.
type personalRule struct {
	marker  string
	re      *regexp.Regexp
	valid   func(match string) bool
	rewrite func(match, marker string) string
}

// personalRules run in order: payment cards before phone numbers, so a card
// is never half-replaced as a phone number.
var personalRules = []personalRule{
	{marker: MarkerCard, re: regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`), valid: luhn},
	{marker: MarkerEmail, re: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)},
	{marker: MarkerIP, re: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), valid: ipv4},
	{marker: MarkerIP, re: regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{1,4}\b`), valid: ipv6},
	// Shortened IPv6 addresses ("2001:db8::1", "::1"): every unbroken run
	// of letters, digits and colons is a candidate, and only the address in
	// it is replaced (see withoutShortIPv6), so "std::string" and "12:30:45"
	// stay. Text that happens to be a valid address ("a::b", or "abc::def"
	// in "sha256:abc::def") is replaced as well: hiding too much is the
	// safer mistake.
	{marker: MarkerIP, re: regexp.MustCompile(`[0-9A-Za-z:]+`), rewrite: withoutShortIPv6},
	// International numbers, then common national formats.
	{marker: MarkerPhone, re: regexp.MustCompile(`\+\d[\d ().\-]{6,18}\d`), valid: phoneDigits},
	{marker: MarkerPhone, re: regexp.MustCompile(`\(\d{3}\) ?\d{3}[ .\-]\d{4}\b`)},
	{marker: MarkerPhone, re: regexp.MustCompile(`\b\d{3}[.\-]\d{3}[.\-]\d{4}\b`)},
	{marker: MarkerPhone, re: regexp.MustCompile(`\b0\d{2,3}[ .\-]\d{3}[ .\-]\d{3,4}\b`)},
}

// personalData replaces every recognised piece of personal data.
func personalData(text string) string {
	for _, rule := range personalRules {
		text = rule.re.ReplaceAllStringFunc(text, func(m string) string {
			if rule.rewrite != nil {
				return rule.rewrite(m, rule.marker)
			}
			if rule.valid != nil && !rule.valid(m) {
				return m
			}
			return rule.marker
		})
	}
	return text
}

func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// luhn reports whether the digits pass the payment-card checksum.
func luhn(s string) bool {
	d := digitsOf(s)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum := 0
	for i := range len(d) {
		n := int(d[len(d)-1-i] - '0')
		if i%2 == 1 {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
	}
	return sum%10 == 0
}

func ipv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is4()
}

func ipv6(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is6()
}

// shortIPv6 reports whether s is an IPv6 address written with "::" that
// names at least one group; a bare "::" is punctuation far more often than
// the unspecified address.
func shortIPv6(s string) bool {
	return strings.Contains(s, "::") && strings.Trim(s, ":") != "" && ipv6(s)
}

// maxIPv6Text is the longest IPv6 address in letters, digits and colons:
// eight groups of four hex digits and seven colons.
const maxIPv6Text = 39

// withoutShortIPv6 returns run, an unbroken run of letters, digits and
// colons, with every IPv6 address written with "::" replaced by marker:
// the whole run, or an address between labels, as "2001:db8::5" in
// "2001:db8::5:" or "2001:db8::1::", "::1" in "ip:::1", "fe80::1" in
// "fe80::1:eth0:up" and both addresses in "src:fe80::1:dst:fe80::2". An
// address starts at the start of the run or after a "label:", whose colon
// is not preceded by another. It ends at the end of the run, before the
// colons that end the run, or before a ":label", whose colon is not
// followed by another. So the "::" of an address is never split, and
// "std::string" or "1::2::3" hold no address. The run is read from the
// left, and from each start the longest address is taken. Each start is
// tried with at most maxIPv6Text characters, so the work grows with the
// length of the run and no more.
func withoutShortIPv6(run, marker string) string {
	if !strings.Contains(run, "::") {
		return run
	}
	body := len(strings.TrimRight(run, ":"))
	var out strings.Builder
	written := 0
	for start := 0; start < len(run); start++ {
		afterLabel := start == 0 || run[start-1] == ':' && (start == 1 || run[start-2] != ':')
		if !afterLabel {
			continue
		}
		end := shortIPv6End(run, start, body)
		if end < 0 {
			continue
		}
		out.WriteString(run[written:start])
		out.WriteString(marker)
		written = end
		start = end - 1
	}
	out.WriteString(run[written:])
	return out.String()
}

// shortIPv6End gives where the longest IPv6 address written with "::" that
// starts at start ends in run, or -1 when none starts there. body is the
// length of run without the colons that end it.
func shortIPv6End(run string, start, body int) int {
	limit := min(len(run), start+maxIPv6Text)
	if !strings.Contains(run[start:limit], "::") {
		return -1
	}
	for end := limit; end > start; end-- {
		beforeLabel := end == len(run) || run[end] == ':' && (end >= body || run[end+1] != ':')
		if beforeLabel && shortIPv6(run[start:end]) {
			return end
		}
	}
	return -1
}

func phoneDigits(s string) bool {
	n := len(digitsOf(s))
	return n >= 8 && n <= 15
}
