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
	// of letters, digits and colons is a candidate, and only the addresses
	// in it are replaced (see withoutShortIPv6), so "std::string" and
	// "12:30:45" stay. Text that happens to be a valid address ("a::b", or
	// "abc::def" in "sha256:abc::def") is replaced as well: hiding too much
	// is the safer mistake.
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
// colons, without the IPv6 addresses written with "::" in it. Every piece
// of the run that starts at the start of the run or after a colon, ends at
// the end of the run or before a colon, and is such an address is hidden;
// pieces that overlap or touch are hidden together, under one marker. So are the
// whole run "2001:db8::1", "2001:db8::5" in "2001:db8::5:", "::1" in
// "ip:::1", "fe80::1" in "fe80::1:eth0:up", both addresses in
// "src:fe80::1:dst:fe80::2", and all of "12:30:45:fe80::1:2:3:4:5", where
// the address and the numbers before it cannot be told apart. "std::string"
// and "12:30:45" hold no such piece. An address is at most maxIPv6Text
// characters, so each start is tried with no more, and the work grows with
// the length of the run and no faster.
func withoutShortIPv6(run, marker string) string {
	if !strings.Contains(run, "::") {
		return run
	}
	var out strings.Builder
	written := 0       // run[:written] is in out
	from, to := -1, -1 // the piece being hidden, not yet in out
	for start := 0; start < len(run); start++ {
		if start > 0 && run[start-1] != ':' {
			continue
		}
		end := shortIPv6End(run, start)
		if end < 0 {
			continue
		}
		if start <= to {
			to = max(to, end)
			continue
		}
		if from >= 0 {
			out.WriteString(run[written:from])
			out.WriteString(marker)
			written = to
		}
		from, to = start, end
	}
	if from >= 0 {
		out.WriteString(run[written:from])
		out.WriteString(marker)
		written = to
	}
	out.WriteString(run[written:])
	return out.String()
}

// shortIPv6End gives where the longest IPv6 address written with "::" that
// starts at start ends in run, at the end of run or before a colon, or -1
// when none starts there.
func shortIPv6End(run string, start int) int {
	// An address starts with a hex digit or with "::" and a hex digit, and
	// holds nothing but hex digits and colons.
	if run[start] == ':' && (start+2 >= len(run) || run[start+1] != ':' || run[start+2] == ':') {
		return -1
	}
	limit := min(len(run), start+maxIPv6Text)
	for i := start; i < limit; i++ {
		if !hexOrColon(run[i]) {
			limit = i
			break
		}
	}
	first := strings.Index(run[start:limit], "::")
	if first < 0 {
		return -1
	}
	for end := limit; end >= start+first+2; end-- {
		if (end == len(run) || run[end] == ':') && shortIPv6(run[start:end]) {
			return end
		}
	}
	return -1
}

func hexOrColon(c byte) bool {
	return c == ':' || '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func phoneDigits(s string) bool {
	n := len(digitsOf(s))
	return n >= 8 && n <= 15
}
