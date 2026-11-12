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

// Candidate branches earlier runs left are reported, never deleted.
func TestLeftoverCandidateBranchesAreReported(t *testing.T) {
	t.Parallel()
	if got := leftoverCandidateBranches(""); got != "none" {
		t.Fatalf("no branches: %q", got)
	}
	got := leftoverCandidateBranches("refs/heads/sisf/f1/run-a-1\nrefs/heads/sisf/f1/run-b-2\n")
	if !strings.Contains(got, "2 from earlier runs") || !strings.Contains(got, "sisf/f1/run-a-1, sisf/f1/run-b-2") {
		t.Fatalf("leftoverCandidateBranches = %q", got)
	}
}

// Once a release is verified the factory opens a pull request that records
// it in the changelog, for the Owner to merge. The run closes the one of
// its own runs, so the next run starts from an idle sandbox, and touches no
// other pull request.
func TestTheRunClosesOnlyTheReleaseRecordOfItsOwnRun(t *testing.T) {
	t.Parallel()
	pulls := `[{"number":11,"headRefName":"sisf/release/run-a"},{"number":12,"headRefName":"sisf/release/run-b"},` +
		`{"number":13,"headRefName":"sisf/sisf-sandbox/run-a-1"},{"number":14,"headRefName":"feature/run-a"}]`
	got, err := releaseRecordPulls(pulls, []string{"run-a"})
	if err != nil || len(got) != 1 || got[0] != 11 {
		t.Fatalf("release record pull requests of run-a: %v, %v", got, err)
	}
	if got, err := releaseRecordPulls(pulls, nil); err != nil || len(got) != 0 {
		t.Fatalf("with no run: %v, %v", got, err)
	}
	if _, err := releaseRecordPulls(`{`, []string{"run-a"}); err == nil {
		t.Fatal("unreadable JSON was accepted")
	}
}
