package control

import (
	"html"
	"strconv"
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
	// A reference assembled only once an invisible character between its
	// parts is dropped is decoded too: the secret is split once, so the
	// title is not cut before it.
	for _, ref := range []string{"&#&shy;x200b;", "&s&#8203;hy;", "&#&#8203;x200b;"} {
		title := reportIssueTitle(report.Report{Title: "Login " + secret[:10] + ref + secret[10:]}, redactor.Redact)
		if shown := joined(html.UnescapeString(title)); strings.Contains(shown, secret[:12]) {
			t.Errorf("%s: the title shows the split secret: %q", ref, title)
		}
	}
	// A reference that names a mention is defused like the mention itself.
	if title := reportIssueTitle(report.Report{Title: "ping &#64;octocat"}, redactor.Redact); strings.Contains(title, "@octocat") {
		t.Errorf("a referenced @ formed a mention: %q", title)
	}
	// An address whose @ is written as a reference is redacted although
	// defusing breaks the @ it decodes to.
	for _, at := range []string{"&#64;", "&commat;", "&amp;#64;"} {
		title := reportIssueTitle(report.Report{Title: "mail bob" + at + "example.com"}, redactor.Redact)
		if shown := joined(html.UnescapeString(title)); strings.Contains(shown, "bob@example.com") || strings.Contains(shown, "example.com") {
			t.Errorf("%s: the title shows the address: %q", at, title)
		}
	}
	// So is a configured secret holding an @ and a # before a digit,
	// written as references.
	configured := "fake-Secret@value#1x"
	withSecret := redact.New(redact.Config{Secrets: []string{configured}})
	for _, written := range []string{"fake-Secret&#64;value&#35;1x", "fake-Secret&commat;value&num;1x", "fake-Secret&amp;#64;value&amp;#35;1x"} {
		title := reportIssueTitle(report.Report{Title: "Login " + written}, withSecret.Redact)
		if shown := joined(html.UnescapeString(title)); strings.Contains(shown, "fake-Secret") || strings.Contains(shown, "value#1x") {
			t.Errorf("%s: the title shows the configured secret: %q", written, title)
		}
	}
}

// Defusing puts a zero-width space after an @ or a # before a digit, so
// text is redacted before it is defused as well as after: an address, or a
// configured secret holding such characters, is found whole in a report's
// title and body.
func TestTextIsRedactedBeforeDefusingBreaksIt(t *testing.T) {
	t.Parallel()
	secret := "fake-Secret@value#1x"
	redactor := redact.New(redact.Config{Secrets: []string{secret}})
	in := report.Report{ID: "report-0123456789abcdef", Title: "Login " + secret + " bob@example.com",
		Body: "token " + secret + "\nmail bob@example.com"}
	title, body := reportIssueTitle(in, redactor.Redact), reportIssueBody(in, redactor.Redact)
	for name, text := range map[string]string{"title": title, "body": body} {
		shown := joined(text)
		if strings.Contains(shown, "value#1x") || strings.Contains(shown, "fake-Secret") || strings.Contains(shown, "example.com") {
			t.Errorf("the %s shows the secret or the address:\n%s", name, text)
		}
	}
}

// A control character between the parts of a character reference, itself
// written as a reference or encoded twice, is dropped while references are
// decoded, not only afterwards, so the reference it hid is decoded too: the
// title holds no reference GitHub would show as a character, and the
// redactor sees the text as GitHub shows it.
func TestATitleReferenceAssembledAroundAControlCharacterIsDecoded(t *testing.T) {
	t.Parallel()
	redactor := redact.New(redact.Config{})
	for _, tc := range []struct{ title, want string }{
		{"a&amp;#&amp;#1;x6B;b", "akb"},
		{"a&#&#1;x6B;b", "akb"},
		{"a&amp;amp;#&amp;amp;#1;x6B;b", "akb"},
		{"a&#&#x7f;&#&#1;x31;07;b", "akb"},
		{"a&#\x01x6B;b", "akb"},
	} {
		title := reportIssueTitle(report.Report{Title: tc.title}, redactor.Redact)
		if want := reportIssueTitlePrefix + tc.want; title != want {
			t.Errorf("%q: title %q, want %q", tc.title, title, want)
		}
		if html.UnescapeString(title) != title {
			t.Errorf("%q: the title %q still holds a character reference", tc.title, title)
		}
	}
	secret := renderToken()
	for _, hide := range []string{"&amp;#&amp;#1;x%x;", "&#&#1;x%x;", "&#&#x1f;%d;", "&amp;#&amp;#127;%d;"} {
		ref := strings.Replace(hide, "%x", strconvHex(secret[10]), 1)
		ref = strings.Replace(ref, "%d", strconvDec(secret[10]), 1)
		title := reportIssueTitle(report.Report{Title: "Login " + secret[:10] + ref + secret[11:]}, redactor.Redact)
		if shown := joined(html.UnescapeString(title)); strings.Contains(shown, secret[:12]) {
			t.Errorf("%s: the title shows the secret: %q", ref, title)
		}
	}
}

func strconvHex(b byte) string { return strconv.FormatInt(int64(b), 16) }
func strconvDec(b byte) string { return strconv.Itoa(int(b)) }
