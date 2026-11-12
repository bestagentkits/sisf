package redact

import (
	"net/netip"
	"regexp"
	"strings"
)

// personalRule finds one kind of personal data. valid, when set, confirms a
// candidate match, so numbers that merely look alike are left alone. within,
// when set instead, gives the part of a candidate match that is personal
// data, or ok false when none is; only that part is replaced.
type personalRule struct {
	marker string
	re     *regexp.Regexp
	valid  func(match string) bool
	within func(match string) (start, end int, ok bool)
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
	// it is replaced (see shortIPv6Within), so "std::string" and "12:30:45"
	// stay. Text that happens to be a valid address ("a::b") is replaced as
	// well: hiding too much is the safer mistake.
	{marker: MarkerIP, re: regexp.MustCompile(`[0-9A-Za-z:]+`), within: shortIPv6Within},
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
			if rule.within != nil {
				start, end, ok := rule.within(m)
				if !ok {
					return m
				}
				return m[:start] + rule.marker + m[end:]
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

// shortIPv6Within finds an IPv6 address written with "::" in run, an
// unbroken run of letters, digits and colons: the whole run, or else what is
// left after one colon that ends the run, a "label:" in front, or both are
// cut, as "2001:db8::5" in "2001:db8::5:" or "::1" in "ip:::1". A label
// ends at a colon not preceded by another, so the "::" of an address is
// never split.
// Only the last maxIPv6Text characters are tried, so the work per run is
// bounded however long it is.
func shortIPv6Within(run string) (start, end int, ok bool) {
	ends := []int{len(run)}
	if strings.HasSuffix(run, ":") {
		ends = append(ends, len(run)-1)
	}
	for _, end := range ends {
		// Longest first: the run up to end, then after each label.
		for start := max(end-maxIPv6Text, 0); start < end; start++ {
			afterLabel := start == 0 || run[start-1] == ':' && (start == 1 || run[start-2] != ':')
			if afterLabel && shortIPv6(run[start:end]) {
				return start, end, true
			}
		}
	}
	return 0, 0, false
}

func phoneDigits(s string) bool {
	n := len(digitsOf(s))
	return n >= 8 && n <= 15
}
