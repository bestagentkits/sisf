package stages

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The login tells the factory's own account. An install may name the
// App's numeric ID, which some other user's ID can equal, so an ID never
// stands in for the login; it narrows the match only when GitHub itself
// named it as the account's user ID.
func TestTheFactorysAccountIsToldByItsLogin(t *testing.T) {
	t.Parallel()
	bot := github.User{ID: 77, Login: "factory[bot]"}
	cases := map[string]struct {
		self   account
		author github.User
		want   bool
	}{
		"named login, same ID":                 {account{User: bot}, bot, true},
		"named login in another case":          {account{User: bot}, github.User{ID: 77, Login: "Factory[Bot]"}, true},
		"named login, the App's ID":            {account{User: github.User{ID: 4242, Login: bot.Login}}, bot, true},
		"the named ID under another login":     {account{User: bot}, github.User{ID: 77, Login: "outsider"}, false},
		"an ID named without a login":          {account{User: github.User{ID: 77}}, bot, false},
		"nothing named":                        {account{}, bot, false},
		"GitHub's own account, same ID":        {account{User: bot, userID: true}, bot, true},
		"GitHub's own account, another ID":     {account{User: bot, userID: true}, github.User{ID: 78, Login: bot.Login}, false},
		"GitHub's own account, its ID only":    {account{User: bot, userID: true}, github.User{ID: 77, Login: "outsider"}, false},
		"named login, an author with no login": {account{User: bot}, github.User{ID: 77}, false},
	}
	for name, tc := range cases {
		if got := tc.self.wrote(tc.author); got != tc.want {
			t.Errorf("%s: wrote = %v, want %v", name, got, tc.want)
		}
	}
}

// Someone whose GitHub user ID equals the ID the install names, as the
// App's ID may, does not get a pull request adopted by copying the run's
// marker: the candidate is left for the Owner and the reason names the
// author and the factory's configured account.
func TestAnIDAloneNeverMakesAPullRequestTheFactorys(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		outsider := w.fake.Person("outsider", "NONE")
		w.prs.self = github.User{ID: outsider.ID, Login: w.fake.Bot().Login}
		foreign, err := w.fake.OpenPullRequest(landingRepo, outsider, fakegithub.PullSpec{
			Title: "Greet", Body: "Fixes #1\n\n" + candidateMarker(w.req.RunID), HeadRef: w.branch, BaseRef: "main",
		})
		if err != nil {
			t.Fatal(err)
		}
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || w.data.Merges() != 0 {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		if !strings.Contains(landing.Summary, "`outsider` is not the factory's configured account `"+w.fake.Bot().Login+"`") {
			t.Errorf("the reason does not name the author and the configured account: %s", landing.Summary)
		}
		if view, err := w.fake.Issue(landingRepo, foreign); err != nil || view.State != "open" {
			t.Fatalf("the foreign pull request is %q, %v", view.State, err)
		}
	})
}

// An install that names the factory's numeric ID but no login cannot tell
// its own pull request from anyone's: it opens none, adopts none, and
// leaves every candidate for the Owner, saying the login is missing.
func TestAnInstallThatNamesNoLoginAdoptsNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		w.prs.self = github.User{ID: w.fake.Bot().ID}
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || w.data.Merges() != 0 || !strings.Contains(landing.Summary, "github_app_login") {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		open, err := w.client.FindPullRequests(ctx, w.prs.Repo, w.branch)
		if err != nil || len(open) != 0 {
			t.Fatalf("pull requests on the branch: %+v, %v", open, err)
		}
		if pr, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1); err != nil || pr.Number != 0 || pr.State != handoff.LandingLeftOpen {
			t.Fatalf("recorded %+v, %v", pr, err)
		}
	})
}

// A wrong login in the install shows as such: the pull request the
// factory opened is left for the Owner with a reason that names its author
// and the account the install configured, not as a pull request someone
// else changed.
func TestAWrongConfiguredLoginIsNamedInTheReason(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		w.prs.self = github.User{Login: "renamed-app[bot]"}
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || w.data.Merges() != 0 {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		want := "`" + w.fake.Bot().Login + "` is not the factory's configured account `renamed-app[bot]`"
		if !strings.Contains(landing.Summary, want) {
			t.Fatalf("the reason does not say the configured account is not the author: %s", landing.Summary)
		}
	})
}

// GitHub may open the factory's pull request and lose the answer, and its
// listing may not show the new pull request yet. The retry's refusal that
// one already exists is then no reason to give the candidate up: the
// factory looks again, adopts its own pull request and merges it.
func TestALostAnswerToOpeningThePullRequestStillAdoptsIt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		w.prs.self = github.User{Login: w.fake.Bot().Login}
		w.data.FailAfter(http.MethodPost, "/pulls", http.StatusBadGateway, "Server Error")
		if _, err := w.prs.Land(ctx, w.req); err == nil {
			t.Fatal("an unanswered open was decided")
		}
		w.data.ListNoPullsFor(1)
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || !landing.Merged || w.data.Merges() != 1 {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		open, err := w.client.FindPullRequests(ctx, w.prs.Repo, w.branch)
		if err != nil || len(open) != 1 {
			t.Fatalf("pull requests on the branch: %d, %v", len(open), err)
		}
	})
}

// When GitHub refuses for good to say which account the factory's token is,
// as for a token that may not read its user, the landing is decided: the
// candidate is left for the Owner with that reason and the issue can
// close, rather than staying verified for ever.
func TestAPermanentRefusalToNameTheFactorysAccountLeavesTheCandidate(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		w.data.FailOn(http.MethodGet, "/user", http.StatusForbidden, "Resource not accessible by integration")
		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || w.data.Merges() != 0 || !strings.Contains(landing.Summary, "refused to name the factory's own account (status 403") {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
	})
}

// A pull request the factory opened with an earlier build, which wrote no
// marker but named the run, is still the factory's own and is merged; one
// someone else opened with the same text is not.
func TestAPullRequestOfAnEarlierBuildIsAdoptedByItsAuthorAndRun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		earlier := func(w *landingWorld) string {
			return "Candidate of run `" + string(w.req.RunID) + "` for #1, built and judged by the factory.\n\nRefs #1\n\n" +
				"The factory merges this pull request itself, at exactly this commit, once the release built from it is verified in production.\n"
		}
		t.Run("opened by the factory", func(t *testing.T) {
			w := landingFactory(t, db, "earlier build")
			if _, err := w.fake.OpenPullRequest(landingRepo, w.fake.Bot(), fakegithub.PullSpec{
				Title: "Greet", Body: earlier(w), HeadRef: w.branch, BaseRef: "main"}); err != nil {
				t.Fatal(err)
			}
			if landing, err := w.prs.Land(ctx, w.req); err != nil || !landing.Merged || w.data.Merges() != 1 {
				t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
			}
		})
		t.Run("copied by someone else", func(t *testing.T) {
			w := landingFactory(t, db, "earlier copied")
			if _, err := w.fake.OpenPullRequest(landingRepo, w.fake.Person("outsider", "NONE"), fakegithub.PullSpec{
				Title: "Greet", Body: earlier(w), HeadRef: w.branch, BaseRef: "main"}); err != nil {
				t.Fatal(err)
			}
			if landing, err := w.prs.Land(ctx, w.req); err != nil || landing.Merged || w.data.Merges() != 0 {
				t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
			}
		})
	})
}
