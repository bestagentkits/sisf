package manifest_test

import (
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
