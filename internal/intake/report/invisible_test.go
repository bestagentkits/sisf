package report_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
)

// invisibleRanges are the code points that show nothing although they are
// not control or format characters (Unicode category Cf): the
// Default_Ignorable_Code_Point ranges, and the braille blank. Each could
// split a secret past redaction and vanish where the text is shown.
var invisibleRanges = []struct {
	name   string
	lo, hi rune
}{
	{"soft hyphen", 0x00AD, 0x00AD},
	{"combining grapheme joiner", 0x034F, 0x034F},
	{"Arabic letter mark", 0x061C, 0x061C},
	{"Hangul choseong and jungseong fillers", 0x115F, 0x1160},
	{"Khmer inherent vowels", 0x17B4, 0x17B5},
	{"Mongolian variation selectors and vowel separator", 0x180B, 0x180F},
	{"zero-width and directional marks", 0x200B, 0x200F},
	{"directional embeddings and overrides", 0x202A, 0x202E},
	{"word joiner and invisible operators", 0x2060, 0x206F},
	{"Hangul filler", 0x3164, 0x3164},
	{"variation selectors", 0xFE00, 0xFE0F},
	{"byte order mark", 0xFEFF, 0xFEFF},
	{"halfwidth Hangul filler", 0xFFA0, 0xFFA0},
	{"unassigned specials", 0xFFF0, 0xFFF8},
	{"shorthand format controls", 0x1BCA0, 0x1BCA3},
	{"musical symbol format controls", 0x1D173, 0x1D17A},
	{"tags and variation selectors supplement", 0xE0000, 0xE0FFF},
	{"braille blank", 0x2800, 0x2800},
}

// fakeToken builds, at run time, a value with a shape the redactor knows,
// so no credential-like literal is kept in the repository.
func fakeToken() string { return "gh" + "p_" + strings.Repeat("A1b2", 9) }

// A secret split by any character that shows nothing is found whole and
// redacted, from the first to the last code point of every such range, and
// a text of nothing but those characters is blank.
func TestEveryInvisibleCodePointIsDroppedBeforeRedaction(t *testing.T) {
	t.Parallel()
	redactor := redact.New(redact.Config{})
	secret := fakeToken()
	for _, tc := range invisibleRanges {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, r := range []rune{tc.lo, (tc.lo + tc.hi) / 2, tc.hi} {
				split := strings.Join(strings.Split(secret, ""), string(r))
				got := redactor.Redact(report.Visible("token " + split))
				if strings.Contains(got, secret) || strings.ContainsRune(got, r) {
					t.Errorf("U+%04X: the split secret survived as %q", r, got)
				}
				if blank := report.Visible(string([]rune{r, r})); blank != "" {
					t.Errorf("U+%04X: Visible kept %q", r, blank)
				}
			}
		})
	}
	if got := report.Visible("line\tone\nline two"); got != "line\tone\nline two" {
		t.Fatalf("Visible changed line breaks or tabs: %q", got)
	}
	for _, r := range []rune{'a', 'é', '中', ' ', 0x2801, 0x3165} {
		if got := report.Visible(string(r)); got != string(r) {
			t.Errorf("Visible dropped the visible U+%04X: %q", r, got)
		}
	}
}
