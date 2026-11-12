package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// fakeCommits compares commits as GitHub's three-dot comparison does: the
// paths head changes since it parted from base, none when base contains
// head. Unless a test says otherwise every commit contains every other.
type fakeCommits struct {
	mu sync.Mutex
	// apart lists, by head, the paths it changes that base does not have.
	apart map[core.CommitSHA][]string
	err   error
	asked int
}

func (c *fakeCommits) ChangedPaths(_ context.Context, base, head core.CommitSHA) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked++
	if c.err != nil {
		return nil, c.err
	}
	if base == head {
		return nil, nil
	}
	return c.apart[head], nil
}

// lkgCommit is the commit of the release the LKG names in these tests.
const lkgCommit core.CommitSHA = "fedcba9876543210fedcba9876543210fedcba98"

// promoteLKGAt records a release of commit as the LKG of the unit, as the
// pipeline records a verified release that serves.
func (w *world) promoteLKGAt(commit core.CommitSHA) {
	w.t.Helper()
	issue, run := issueRef(299), core.RunID("run-299")
	c := candidate(issue, run, secondBuild)
	c.CommitSHA = commit
	receipt := w.keys.receiptFor(w.t, c, nil)
	if err := w.book.record(verifiedRelease{issue: issue, run: run, artifact: w.keys.artifactFor(w.t, secondBuild), receipt: receipt,
		verifiedAt: w.clock.Now(), sequence: 1}); err != nil {
		w.t.Fatal(err)
	}
	w.serve(core.ReleaseDigest(secondBuild))
}

// The LKG keeps the commit of the release it names.
func TestTheLKGKeepsTheCommitOfItsRelease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.promoteLKGAt(lkgCommit)
	if lkg := w.lkg(); lkg.Commit != lkgCommit {
		t.Fatalf("the LKG is %+v, want it to name commit %s", lkg, lkgCommit)
	}
}

// A candidate built on a base that does not contain the commit of its
// unit's LKG release would drop that release's change: it is refused for
// the Owner before anything is deployed, and the reason names both.
func TestACandidateWhoseBaseLacksTheLKGCommitIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.promoteLKGAt(lkgCommit)
	w.commits.apart = map[core.CommitSHA][]string{lkgCommit: {"examples/hello-app/main.go"}}
	issue, run := issueRef(270), core.RunID("run-270")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeApprovalRequired) || out.Result != ResultRefused || out.Cause != statemachine.BlockReleaseRefused {
		t.Fatalf("Release = %+v, %v; want refused for the Owner", out, err)
	}
	if !strings.Contains(out.Reason, string(lkgCommit)) || !strings.Contains(out.Reason, string(commitSHA)) {
		t.Fatalf("the refusal %q does not name the base and the LKG's commit", out.Reason)
	}
	w.requireState(issue, core.StateBlocked)
	if _, _, staged, _ := w.backend.snapshot(); len(staged) != 0 {
		t.Fatalf("%v was deployed", staged)
	}
}

// A base that contains the LKG's commit is released; so is any candidate
// while the LKG is the release the install pins, which names no commit.
func TestACandidateWhoseBaseContainsTheLKGCommitIsReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		promote bool
	}{{"the base contains it", true}, {"the install pins the lkg", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			if tc.promote {
				w.promoteLKGAt(lkgCommit)
			}
			issue, run := issueRef(271), core.RunID("run-271")
			w.seedReleaseCandidate(issue, run, firstBuild)
			if out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild)); err != nil || out.Result != ResultClosed {
				t.Fatalf("Release = %+v, %v; want closed", out, err)
			}
			if asked := w.commits.asked; tc.promote != (asked > 0) {
				t.Fatalf("GitHub was asked %d times", asked)
			}
		})
	}
}

// A comparison GitHub cuts short, or refuses, cannot show the base holds
// the LKG's commit: the candidate is refused. One GitHub could not answer
// decides nothing: the release waits and nothing is deployed.
func TestABaseThatCannotBeComparedWithTheLKGCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		err     error
		refused bool
	}{
		{"github cuts it short", core.NewError(core.CodeLimitExceeded, "too many files"), true},
		{"github does not know the commit", core.NewError(core.CodeNotFound, "no such commit"), true},
		{"github does not answer", core.NewError(core.CodeUnavailable, "GitHub is out"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.promoteLKGAt(lkgCommit)
			w.commits.err = tc.err
			issue, run := issueRef(272), core.RunID("run-272")
			w.seedReleaseCandidate(issue, run, firstBuild)
			out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
			if tc.refused {
				if !core.HasCode(err, core.CodeApprovalRequired) || out.Result != ResultRefused {
					t.Fatalf("Release = %+v, %v; want refused for the Owner", out, err)
				}
				w.requireState(issue, core.StateBlocked)
			} else {
				if !core.HasCode(err, core.CodeUnavailable) || out.Result == ResultRefused {
					t.Fatalf("Release = %+v, %v; want it to wait", out, err)
				}
				w.requireState(issue, core.StateReleaseCandidate)
			}
			if _, _, staged, _ := w.backend.snapshot(); len(staged) != 0 {
				t.Fatalf("%v was deployed", staged)
			}
		})
	}
}

// unreadable makes the file at path one its owner may not read. Root reads
// it all the same, so a test that needs it is skipped as root.
func unreadable(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
}

// An LKG that cannot be read now cannot show the base contains its commit:
// the check makes the release wait, UNAVAILABLE, and it is tried again. An
// entry that is not one the supervisor wrote holds the release for the
// Owner, as the preflight holds it. Neither lets the release through.
func TestABaseCheckWhoseLKGCannotBeReadDoesNotPass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry func(t *testing.T, path string)
		code  core.ErrorCode
	}{
		{"the entry cannot be read now", unreadable, core.CodeUnavailable},
		{"the entry was tampered with", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"unit":"`+testUnit+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, core.CodeReleaseHold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.promoteLKGAt(lkgCommit)
			tc.entry(t, filepath.Join(w.trustDir, "lkg-unit-"+testUnit+".json"))
			v := verifiedRelease{artifact: w.keys.artifactFor(t, firstBuild), receipt: core.QAReceipt{PolicySHA: commitSHA}}
			err := w.pipeline().checkBaseHasLKG(context.Background(), v)
			if !core.HasCode(err, tc.code) || judgedRefusal(context.Background(), err) {
				t.Fatalf("checkBaseHasLKG = %v; want %s, a hold", err, tc.code)
			}
			if w.commits.asked != 0 {
				t.Fatalf("GitHub was asked %d times without an LKG to compare with", w.commits.asked)
			}
		})
	}
}

// A release whose LKG cannot be read now waits in ReleaseCandidate and is
// tried again; nothing is deployed.
func TestAReleaseWhoseLKGCannotBeReadNowWaits(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.promoteLKGAt(lkgCommit)
	unreadable(t, filepath.Join(w.trustDir, "lkg-unit-"+testUnit+".json"))
	issue, run := issueRef(273), core.RunID("run-273")
	w.seedReleaseCandidate(issue, run, firstBuild)
	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeUnavailable) || out.Result == ResultRefused {
		t.Fatalf("Release = %+v, %v; want it to wait", out, err)
	}
	w.requireState(issue, core.StateReleaseCandidate)
	if _, _, staged, _ := w.backend.snapshot(); len(staged) != 0 {
		t.Fatalf("%v was deployed", staged)
	}
}
