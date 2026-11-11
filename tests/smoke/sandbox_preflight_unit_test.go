//go:build smoke

package smoke

import (
	"strings"
	"testing"
)

// A fresh install reconciles every open issue of the sandbox, so a paid
// run starts only from a sandbox with no open factory work: no open issue
// carrying a sisf: label and no open pull request.
func TestThePaidRunRefusesASandboxWithOpenFactoryWork(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		issues, pulls string
		want          string // part of the refusal; "" means clean
	}{
		"clean": {`[{"number":3,"labels":[{"name":"kind:improvement"}]}]`, `[]`, ""},
		"an open issue labelled for the factory": {`[{"number":3,"labels":[{"name":"kind:bug"},{"name":"sisf:ready"}]}]`, `[]`,
			"issue #3 (sisf:ready)"},
		"an issue the factory parked": {`[{"number":4,"labels":[{"name":"sisf:needs-info"}]}]`, `[]`, "issue #4"},
		"an open pull request":        {`[]`, `[{"number":7}]`, "pull request #7"},
		"unreadable answer":           {`not json`, `[]`, "cannot be read"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := openFactoryWork(c.issues, c.pulls)
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Fatalf("openFactoryWork = %q, want %q", got, c.want)
			}
		})
	}
}
