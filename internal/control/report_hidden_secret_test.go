package control_test

import (
	"net/http"
	"strings"
	"testing"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// fakeSecrets builds, at run time, values with the shapes the redactor
// knows, so no credential-like literal is ever kept in the repository.
func fakeSecrets() []string {
	return []string{
		"gh" + "p_" + strings.Repeat("A1b2", 9),
		"s" + "k-" + strings.Repeat("x9Y8", 5),
		"AK" + "IA" + strings.Repeat("Q7", 8),
	}
}

// interleave puts sep between every two runes of s.
func interleave(s, sep string) string {
	return strings.Join(strings.Split(s, ""), sep)
}

// visible drops the control and format characters a reader of GitHub does
// not see, so a secret split by them is found whole.
func visible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// A secret split by invisible characters, a zero-width space, a soft
// hyphen or a byte order mark, is redacted all the same: the published
// issue shows no such secret, whether or not those characters are read.
func TestASecretSplitByInvisibleCharactersIsNotPublished(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		var lines []string
		for _, secret := range fakeSecrets() {
			lines = append(lines, "plain "+secret)
			for _, sep := range []string{"\u200b", "\u00ad", "\ufeff"} {
				lines = append(lines, "split "+interleave(secret, sep))
			}
		}
		title := "Login fails " + interleave(fakeSecrets()[0], "\u200b")
		if code, _ := sendReportText(t, e, title, strings.Join(lines, "\n"), 1); code != http.StatusAccepted {
			t.Fatalf("the report: %d", code)
		}
		publish(t, p)
		issues := gh.created()
		if len(issues) != 1 {
			t.Fatalf("%d issues opened, want 1", len(issues))
		}
		for name, text := range map[string]string{"title": issues[0].Title, "body": issues[0].Body} {
			for _, secret := range fakeSecrets() {
				if strings.Contains(text, secret) || strings.Contains(visible(text), secret) {
					t.Errorf("the %s publishes a secret:\n%s", name, text)
				}
			}
		}
	})
}
