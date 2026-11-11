package control

import (
	"html"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
)

// renderToken builds, at run time, a value with a shape the redactor
// knows, so no credential-like literal is kept in the repository.
func renderToken() string { return "gh" + "p_" + strings.Repeat("A1b2", 9) }

// joined drops what a reader of GitHub does not see, the zero-width space
// defusing inserts included, so a secret split by such characters is found
// whole.
func joined(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || report.Invisible(r) {
			return -1
		}
		return r
	}, s)
}

// Each range of characters that show nothing, not only the format
// characters, is dropped when a report is defused for GitHub, so a secret
// split by any of them is redacted in the issue's title and body: the
// first and the last code point of every range is tried.
func TestDefusingDropsEveryInvisibleCodePointBeforeRedaction(t *testing.T) {
	t.Parallel()
	redactor := redact.New(redact.Config{})
	secret := renderToken()
	ranges := [][2]rune{
		{0x00AD, 0x00AD}, {0x034F, 0x034F}, {0x061C, 0x061C}, {0x115F, 0x1160}, {0x17B4, 0x17B5},
		{0x180B, 0x180F}, {0x200B, 0x200F}, {0x202A, 0x202E}, {0x2060, 0x206F}, {0x3164, 0x3164},
		{0xFE00, 0xFE0F}, {0xFEFF, 0xFEFF}, {0xFFA0, 0xFFA0}, {0xFFF0, 0xFFF8}, {0x1BCA0, 0x1BCA3},
		{0x1D173, 0x1D17A}, {0xE0000, 0xE0FFF}, {0x2800, 0x2800},
	}
	for _, rg := range ranges {
		for _, r := range []rune{rg[0], rg[1]} {
			split := strings.Join(strings.Split(secret, ""), string(r))
			// The report is built as an older route might have stored it,
			// without the characters dropped on the way in.
			in := report.Report{ID: "report-0123456789abcdef", Title: "Login " + split, Body: "token " + split}
			title, body := reportIssueTitle(in, redactor.Redact), reportIssueBody(in, redactor.Redact)
			for name, text := range map[string]string{"title": title, "body": body} {
				if strings.Contains(joined(text), secret) || strings.ContainsRune(text, r) {
					t.Errorf("U+%04X: the %s shows the split secret:\n%s", r, name, text)
				}
			}
		}
	}
}

// The issue's title is not fenced, so GitHub may show an HTML character
// reference in it as the character it names: a reference to an invisible
// character, decimal, hexadecimal, named or encoded twice, is decoded and
// dropped before redaction, so it cannot rebuild a split secret.
func TestATitleSplitByCharacterReferencesIsRedacted(t *testing.T) {
	t.Parallel()
	redactor := redact.New(redact.Config{})
	secret := renderToken()
	for _, ref := range []string{"&#8203;", "&#x200b;", "&#X200B;", "&shy;", "&ZeroWidthSpace;", "&amp;#8203;", "&#10240;"} {
		split := strings.Join(strings.Split(secret, ""), ref)
		title := reportIssueTitle(report.Report{Title: "Login " + split}, redactor.Redact)
		// Shown as GitHub may show it, references decoded, the title holds
		// no part of the secret that says what it is.
		if shown := joined(html.UnescapeString(title)); strings.Contains(shown, secret[:12]) {
			t.Errorf("%s: the title shows the split secret: %q", ref, title)
		}
	}
	// A reference that names a mention is defused like the mention itself.
	if title := reportIssueTitle(report.Report{Title: "ping &#64;octocat"}, redactor.Redact); strings.Contains(title, "@octocat") {
		t.Errorf("a referenced @ formed a mention: %q", title)
	}
}
