package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// landingFactory opens a fresh factory store for one subtest.
func landingFactory(t *testing.T, db *sqlstoretest.Database, name string) *landingWorld {
	t.Helper()
	return newLandingWorld(t, db.OpenFactory(t, core.FactoryID("f-"+strings.ReplaceAll(name, " ", "-")), core.ZoneControl))
}

// What GitHub answers as the supervisor lands a candidate decides the
// record: a merge GitHub made counts as merged whatever its answer was,
// a refusal GitHub means as "try again" is tried again a bounded number of
// times, and a pull request the factory did not open is never adopted.
func TestALandingIsDecidedByWhatGitHubDidNotByItsAnswer(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		notes := func(w *landingWorld, pr handoff.CandidatePR) int {
			comments, _ := w.fake.Comments(landingRepo, pr.Number)
			return len(comments)
		}
		t.Run("the answer to a made merge is lost", func(t *testing.T) {
			w := landingFactory(t, db, "lost answer")
			w.data.FailAfter(http.MethodPut, "/merge", http.StatusBadGateway, "Server Error")
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || !landing.Merged || w.data.Merges() != 1 {
				t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
			}
			pr, _ := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1)
			if pr.State != handoff.LandingMerged || notes(w, pr) != 0 {
				t.Fatalf("recorded %+v with %d notes", pr, notes(w, pr))
			}
		})
		t.Run("merged before a restart, then the policy changed", func(t *testing.T) {
			w := landingFactory(t, db, "merged then policy")
			w.data.FailAfter(http.MethodPut, "/merge", http.StatusBadGateway, "Server Error")
			for range 3 { // every retry of the client's request fails too
				w.data.FailOn(http.MethodPut, "/merge", http.StatusBadGateway, "Server Error")
			}
			if _, err := w.prs.Land(ctx, w.req); err == nil {
				t.Fatal("an unanswered merge was decided")
			}
			w.inForce = "3333333333333333333333333333333333333333"
			if landing, err := w.prs.Land(ctx, w.req); err != nil || !landing.Merged || w.data.Merges() != 1 {
				t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
			}
		})
		t.Run("the base branch was modified", func(t *testing.T) {
			w := landingFactory(t, db, "base modified")
			w.data.FailOn(http.MethodPut, "/merge", http.StatusMethodNotAllowed, "Base branch was modified. Review and try the merge again.")
			if _, err := w.prs.Land(ctx, w.req); !core.HasCode(err, core.CodeUnavailable) {
				t.Fatalf("Land = %v, want it tried again later", err)
			}
			if landing, err := w.prs.Land(ctx, w.req); err != nil || !landing.Merged || w.data.Merges() != 1 {
				t.Fatalf("the second try: %+v, %v", landing, err)
			}
		})
		t.Run("GitHub keeps asking to try again", func(t *testing.T) {
			w := landingFactory(t, db, "keeps asking")
			for range maxMergeRetries + 1 {
				w.data.FailOn(http.MethodPut, "/merge", http.StatusMethodNotAllowed, "Base branch was modified. Review and try the merge again.")
			}
			for range maxMergeRetries {
				if _, err := w.prs.Land(ctx, w.req); err == nil {
					t.Fatal("decided before the retries ran out")
				}
			}
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || landing.Merged || !strings.Contains(landing.Summary, "status 405") {
				t.Fatalf("after %d retries: %+v, %v", maxMergeRetries, landing, err)
			}
		})
		t.Run("GitHub refuses to list the branch's pull requests", func(t *testing.T) {
			w := landingFactory(t, db, "list refused")
			w.data.FailOn(http.MethodGet, "/pulls", http.StatusForbidden, "Resource not accessible by integration")
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || landing.Merged || !strings.Contains(landing.Summary, "status 403") || w.data.Merges() != 0 {
				t.Fatalf("Land = %+v, %v", landing, err)
			}
		})
		t.Run("GitHub refuses to show the opened pull request", func(t *testing.T) {
			w := landingFactory(t, db, "show refused")
			opened, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1)
			if err != nil || opened.Number == 0 {
				t.Fatalf("ensure = %+v, %v", opened, err)
			}
			w.data.FailOn(http.MethodGet, "/pulls", http.StatusForbidden, "Resource not accessible by integration")
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || landing.Merged || w.data.Merges() != 0 {
				t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
			}
			if !strings.Contains(landing.Summary, "GitHub refused to show the pull request (status 403") ||
				!strings.Contains(landing.Summary, fmt.Sprintf("#%d", opened.Number)) {
				t.Fatalf("the reason does not say GitHub refused to show the factory's pull request: %s", landing.Summary)
			}
		})
		t.Run("GitHub refuses to open the pull request", func(t *testing.T) {
			w := landingFactory(t, db, "open refused")
			w.data.FailOn(http.MethodPost, "/pulls", http.StatusForbidden, "Resource not accessible by integration")
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || landing.Merged || !strings.Contains(landing.Summary, "status 403") || w.data.Merges() != 0 {
				t.Fatalf("Land = %+v, %v", landing, err)
			}
		})
	})
}

// A pull request someone else opened on the candidate's branch, even one
// that copies the factory's marker, is neither adopted, merged nor closed:
// the candidate is left for the Owner.
func TestAPullRequestTheFactoryDidNotOpenIsNeverAdopted(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		foreign, err := w.fake.OpenPullRequest(landingRepo, w.fake.Person("outsider", "NONE"), fakegithub.PullSpec{
			Title: "Greet", Body: "Fixes #1\n\n" + candidateMarker(w.req.RunID), HeadRef: w.branch, BaseRef: "main",
		})
		if err != nil {
			t.Fatal(err)
		}
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || !strings.Contains(landing.Summary, "not opened by the factory") || w.data.Merges() != 0 {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		if view, err := w.fake.Issue(landingRepo, foreign); err != nil || view.State != "open" {
			t.Fatalf("the foreign pull request is %q, %v", view.State, err)
		}
		if pr, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1); err != nil || pr.Number != 0 || pr.State != handoff.LandingLeftOpen {
			t.Fatalf("recorded %+v, %v", pr, err)
		}
	})
}

// The note that the factory left a pull request for the Owner is made
// even when someone else's comment already carries its marker.
func TestACopiedMarkerDoesNotSuppressTheFactorysNote(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		pr, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1)
		if err != nil {
			t.Fatal(err)
		}
		ref := core.IssueRef{Owner: "acme", Repo: "app", Number: pr.Number}
		if _, err := w.fake.AddComment(landingRepo, pr.Number, w.fake.Person("outsider", "NONE"), "<!-- sisf:landing "+string(pr.RunID)+" -->"); err != nil {
			t.Fatal(err)
		}
		w.inForce = "3333333333333333333333333333333333333333"
		if landing, err := w.prs.Land(ctx, w.req); err != nil || landing.Merged {
			t.Fatalf("Land = %+v, %v", landing, err)
		}
		comments, err := w.client.ListComments(ctx, ref, time.Time{})
		if err != nil || len(comments) != 2 || !strings.Contains(comments[1].Body, "left open for the Owner") {
			t.Fatalf("the pull request's comments: %+v, %v", comments, err)
		}
	})
}

// The pull request quotes the run's Scope of Work digest only when the
// dispatcher's record holds a well-formed one: that record is the agent
// zone's, so anything else is left out rather than published.
func TestThePullRequestQuotesOnlyAWellFormedScopeDigest(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		agentZone, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		for digest, want := range map[core.Digest]core.Digest{
			landingDigest: landingDigest,
			"x`\n\nFixes #12 <!-- sisf:candidate -->": "",
		} {
			raw, err := json.Marshal(dispatcher.RunRecord{RunID: "run-1", Brief: dispatcher.Brief{ScopeDigest: digest}})
			if err != nil {
				t.Fatal(err)
			}
			err = agentZone.Update(ctx, func(ctx context.Context, tx state.Tx) error {
				version := int64(0)
				if e, err := tx.GetKV(ctx, handoff.Namespace, "run/run-1"); err == nil {
					version = e.Version
				}
				_, err := tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: "run/run-1", Value: raw, Version: version})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var got core.Digest
			err = control.View(ctx, func(ctx context.Context, tx state.Tx) error {
				got = scopeOf(ctx, tx, "run-1")
				return nil
			})
			if err != nil || got != want {
				t.Fatalf("scopeOf(%q) = %q, %v", digest, got, err)
			}
		}
	})
}

// An installation token of a GitHub App cannot ask GitHub which account it
// is, so the factory's account comes from the install: with it named,
// the factory's own pull request is found and merged without asking.
func TestTheInstallNamesTheFactorysAccount(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		bot := w.fake.Bot()
		w.prs.self = github.User{ID: bot.ID, Login: bot.Login}
		for range 4 {
			w.data.FailOn(http.MethodGet, "/user", http.StatusForbidden, "Resource not accessible by integration")
		}
		if _, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1); err != nil {
			t.Fatal(err)
		}
		if err := w.prs.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return tx.DeleteKV(ctx, handoff.BuildNamespace, handoff.CandidatePRKey(w.req.RunID), 1) // forgotten, as after a lost write
		}); err != nil {
			t.Fatal(err)
		}
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || !landing.Merged || w.data.Merges() != 1 {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
	})
}

// An install may name the App's numeric ID where GitHub shows the bot
// user's: the login still tells the factory's own pull request.
func TestTheFactorysPullRequestIsKnownByItsLoginWhenTheIDsDiffer(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		w.prs.self = github.User{ID: 4242, Login: w.fake.Bot().Login}
		if _, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1); err != nil {
			t.Fatal(err)
		}
		if landing, err := w.prs.Land(ctx, w.req); err != nil || !landing.Merged {
			t.Fatalf("Land = %+v, %v", landing, err)
		}
	})
}

// The paths a candidate is judged, released and merged on are every path
// of its patch and every path GitHub's comparison lists, once each.
func TestTheChangedPathsAreThePatchsAndGitHubsTogether(t *testing.T) {
	t.Parallel()
	got := unionPaths([]string{"b.go", "a.go"}, []string{"c.go", "a.go"})
	if strings.Join(got, ",") != "a.go,b.go,c.go" {
		t.Fatalf("unionPaths = %v", got)
	}
}
