//go:build smoke

package smoke

import (
	"strings"
	"testing"
	"time"
)

// Whether the factory stayed in the sandbox is judged from what the factory
// itself addressed: its configured repository, the issues of its journal
// and the pull requests of its run. Other work of the same GitHub account
// cannot fail it, and no other repository's name reaches the evidence.
func TestTheSandboxScopeIsJudgedFromTheFactorysOwnRecords(t *testing.T) {
	t.Parallel()
	sandboxIssue, sandboxPR := sandboxRepo+"#1", sandboxURL+"pull/2"
	for name, c := range map[string]struct {
		repo         string
		issues, prs  []string
		pass         bool
		wantEvidence string
	}{
		"only the sandbox":               {sandboxRepo, []string{sandboxIssue}, []string{sandboxPR}, true, "1 issue(s) in its journal and 1 pull request(s)"},
		"another configured repository":  {"someone/private-thing", []string{sandboxIssue}, nil, false, "names another repository"},
		"an issue of another repository": {sandboxRepo, []string{sandboxIssue, "someone/private-thing#4"}, nil, false, "1 of 2 issue(s)"},
		"a pull request elsewhere":       {sandboxRepo, nil, []string{"https://github.com/someone/private-thing/pull/9"}, false, "1 of 1 pull request(s)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pass, evidence := factoryScope(c.repo, c.issues, c.prs)
			if pass != c.pass || !strings.Contains(evidence, c.wantEvidence) {
				t.Fatalf("pass %t, evidence %q", pass, evidence)
			}
			if strings.Contains(evidence, "someone") || strings.Contains(evidence, "private-thing") {
				t.Fatalf("the evidence names another repository: %q", evidence)
			}
		})
	}
}

// The account's public events are only counted: their repositories are
// never listed, and events before the run do not count.
func TestTheAccountsOtherActivityIsCountedWithoutNames(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 11, 1, 40, 0, 0, time.UTC)
	tsv := strings.Join([]string{
		"2026-11-11T03:14:17Z\tsomeone/private-thing\tIssuesEvent",
		"2026-11-11T02:00:00Z\t" + sandboxRepo + "\tIssuesEvent",
		"2026-11-11T01:00:00Z\tsomeone/older\tPushEvent",
		"not an event",
	}, "\n")
	if n := foreignEvents(tsv, start); n != 1 {
		t.Fatalf("counted %d events of other repositories since the start, want 1", n)
	}
}

// The gh login and the operator's home directory are personal data: the
// report and the log carry placeholders in their place.
func TestTheScrubberReplacesTheLoginAndTheHomeDirectory(t *testing.T) {
	t.Parallel()
	s := newSecretScrubber(nil)
	s.addPersonal("Octo-Cat", "<gh-login>")
	s.addPersonal("/Users/someone", "~")
	got := s.clean("gh api users/octo-cat/events --agent-home /Users/someone and /Users/someone/.docker; octo-catalog stays")
	want := "gh api users/<gh-login>/events --agent-home ~ and ~/.docker; octo-catalog stays"
	if got != want {
		t.Fatalf("clean = %q\nwant    %q", got, want)
	}
}
