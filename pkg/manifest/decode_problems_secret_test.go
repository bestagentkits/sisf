package manifest_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A value pasted in the wrong place may be a credential. A problem names its
// line and what was expected, but never quotes a value that has the shape of
// a secret, not even its first characters.
func TestParseNeverQuotesAValueShapedLikeASecret(t *testing.T) {
	t.Parallel()
	valid := exampleYAML(t)
	// Built at run time so no credential-shaped literal sits in the source.
	token := "ghp_" + strings.Repeat("Ab1", 12)
	gitlab := "glpat-" + strings.Repeat("xY9", 7)
	awsKey := "ASIA" + strings.Repeat("Q7", 8)
	cases := map[string]struct {
		yaml   string
		hidden []string
	}{
		"a token for a number": {strings.Replace(valid, "port: 7461", "port: "+token, 1), []string{"ghp_", token[:7]}},
		"a credentialed URL for a number": {strings.Replace(valid, "port: 7461", "port: postgres://app:hunter22@db/x", 1),
			[]string{"postgre", "hunter22"}},
		"a token for a span": {strings.Replace(valid, "run_timeout: 30m", "run_timeout: "+token, 1), []string{"ghp_", token}},
		"a credentialed URL for a span": {strings.Replace(valid, "run_timeout: 30m", "run_timeout: https://ci:hunter22@example.test", 1),
			[]string{"hunter22", "ci:"}},
		"a token as a key": {strings.Replace(valid, "mode: docker", "mode: docker\n"+token+": 1", 1), []string{"ghp_", token}},
		// Shapes the shared redaction helper does not know are withheld too.
		"a GitLab token for a span": {strings.Replace(valid, "run_timeout: 30m", "run_timeout: "+gitlab, 1), []string{"glpat", gitlab}},
		"a GitLab token as a key":   {strings.Replace(valid, "mode: docker", "mode: docker\n"+gitlab+": 1", 1), []string{"glpat", gitlab}},
		"a cloud key as a key":      {strings.Replace(valid, "mode: docker", "mode: docker\n"+awsKey+": 1", 1), []string{awsKey}},
		"a signed URL for a span": {strings.Replace(valid, "run_timeout: 30m", "run_timeout: https://cdn.test/f?sig=Zm9vYmFy", 1),
			[]string{"Zm9vYmFy"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := manifest.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("Parse accepted the input")
			}
			message := err.Error()
			if !strings.Contains(message, "line ") {
				t.Errorf("error %q must still name the line", message)
			}
			for _, secret := range tc.hidden {
				if strings.Contains(message, secret) {
					t.Errorf("error %q quotes %q of a value shaped like a secret", message, secret)
				}
			}
		})
	}
}

// A short value can be a password as well as a long one can be a token, so
// a mistyped value is quoted only when it reads as a number, a true/false
// value, a duration or a lowercase word such as a manifest choice; any
// other is said to be withheld. A repeated key is held to the rule of an
// unknown key. The fixtures are built at run time and obviously fake.
func TestParseQuotesOnlyPlainValuesAndKeys(t *testing.T) {
	t.Parallel()
	valid := exampleYAML(t)
	shortSecret := "Zq" + "7!" + "Lx"
	token := "ghp_" + strings.Repeat("Ab1", 12)
	// The line of the span the case replaces, counted rather than written
	// down, so a comment added to the example does not break the case.
	spanLine := "line " + strconv.Itoa(strings.Count(valid[:strings.Index(valid, "run_timeout: 30m")], "\n")+1)
	cases := map[string]struct {
		yaml   string
		hidden []string
		want   []string
	}{
		"a short mixed value for a number": {strings.Replace(valid, "port: 7461", "port: "+shortSecret, 1),
			[]string{shortSecret, "Zq7"}, []string{"text (its value is withheld) does not fit here"}},
		"a short mixed value for a span": {strings.Replace(valid, "run_timeout: 30m", "run_timeout: "+shortSecret, 1),
			[]string{shortSecret, "Zq7"}, []string{spanLine}},
		"a token repeated as a key": {strings.Replace(valid, "mode: docker", "mode: docker\nextensions:\n  "+token+": 1\n  "+token+": 2", 1),
			[]string{"ghp_", token}, []string{"is repeated", "withheld"}},
		"a plain word for a number": {strings.Replace(valid, "port: 7461", "port: eighty", 1), nil, []string{"text (eighty) does not fit here"}},
		"a plain key repeated": {strings.Replace(valid, "mode: docker", "mode: docker\nextensions:\n  tracing: 1\n  tracing: 2", 1),
			nil, []string{`key "tracing" is repeated`}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := manifest.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("Parse accepted the input")
			}
			message := err.Error()
			for _, secret := range tc.hidden {
				if strings.Contains(message, secret) {
					t.Errorf("error %q quotes %q", message, secret)
				}
			}
			for _, part := range tc.want {
				if !strings.Contains(message, part) {
					t.Errorf("error %q does not say %q", message, part)
				}
			}
		})
	}
}

// A value written over several lines, a block scalar, in a field of the
// wrong kind is withheld like any other value that is not plainly safe:
// the decoder's message carries the line break, which no rewrite may let
// through unchanged. The fixtures are built at run time and obviously fake.
func TestParseWithholdsAMultiLineValue(t *testing.T) {
	t.Parallel()
	valid := exampleYAML(t)
	// block replaces the port with a block scalar of style holding lines,
	// indented under the port's own key.
	block := func(style string, lines ...string) string {
		at := strings.Index(valid, "port: 7461")
		indent := strings.Repeat(" ", at-strings.LastIndex(valid[:at], "\n")-1+2)
		return valid[:at] + "port: " + style + "\n" + indent + strings.Join(lines, "\n"+indent) + valid[at+len("port: 7461"):]
	}
	cases := map[string]struct {
		yaml   string
		hidden []string
	}{
		"a short block for a number":  {block("|", "Zq", "7L"), []string{"Zq", "7L"}},
		"a long block for a number":   {block("|", "hunter"+"22", "ghp_"+strings.Repeat("Ab1", 12)), []string{"hunter", "ghp_"}},
		"a folded block for a number": {block(">", "Zq", "", "7L"), []string{"Zq", "7L"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := manifest.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("Parse accepted the input")
			}
			message := err.Error()
			for _, secret := range tc.hidden {
				if strings.Contains(message, secret) {
					t.Errorf("error %q quotes %q of a multi-line value", message, secret)
				}
			}
			if !strings.Contains(message, "line ") || !strings.Contains(message, withheld) {
				t.Errorf("error %q must name the line and say the value is withheld", message)
			}
		})
	}
}

// withheld is what a problem says in place of a value it does not quote.
const withheld = "withheld"
