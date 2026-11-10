package redact

import (
	"net/netip"
	"regexp"
	"strings"
)

// personalRule finds one kind of personal data. valid, when set, confirms a
// candidate match, so numbers that merely look alike are left alone.
type personalRule struct {
	marker string
	re     *regexp.Regexp
	valid  func(match string) bool
}

// personalRules run in order: payment cards before phone numbers, so a card
// is never half-replaced as a phone number.
var personalRules = []personalRule{
	{MarkerCard, regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`), luhn},
	{MarkerEmail, regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`), nil},
	{MarkerIP, regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), ipv4},
	{MarkerIP, regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{1,4}\b`), ipv6},
	// International numbers, then common national formats.
	{MarkerPhone, regexp.MustCompile(`\+\d[\d ().\-]{6,18}\d`), phoneDigits},
	{MarkerPhone, regexp.MustCompile(`\(\d{3}\) ?\d{3}[ .\-]\d{4}\b`), nil},
	{MarkerPhone, regexp.MustCompile(`\b\d{3}[.\-]\d{3}[.\-]\d{4}\b`), nil},
	{MarkerPhone, regexp.MustCompile(`\b0\d{2,3}[ .\-]\d{3}[ .\-]\d{3,4}\b`), nil},
}

// personalData replaces every recognised piece of personal data.
func personalData(text string) string {
	for _, rule := range personalRules {
		text = rule.re.ReplaceAllStringFunc(text, func(m string) string {
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

func phoneDigits(s string) bool {
	n := len(digitsOf(s))
	return n >= 8 && n <= 15
}
