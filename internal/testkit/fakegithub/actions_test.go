package fakegithub_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
)

func TestChangesMadeFromTestCodeAreVisibleAndAnnounced(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.fake.Person("alice", "OWNER")
	if again := e.fake.Person("alice", "OWNER"); again.ID != alice.ID {
		t.Fatal("the same login must be the same account")
	}
	n, err := e.fake.OpenIssue(testRepo, alice, "Idea", "details", "kind:feature")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := e.fake.AddComment(testRepo, n, alice, "first")
	if err := e.fake.EditComment(testRepo, id, alice, "edited"); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.RemoveLabel(testRepo, n, alice, "kind:feature"); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.SetIssueState(testRepo, n, alice, "closed"); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.SetIssueState(testRepo, n, alice, "closed"); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.SetIssueState(testRepo, n, alice, "reopened"); err == nil {
		t.Fatal("an unknown state must be refused")
	}
	view, _ := e.fake.Issue(testRepo, n)
	if view.State != "closed" || len(view.Labels) != 0 || view.Author.Login != "alice" {
		t.Fatalf("issue = %+v", view)
	}
	comments, _ := e.fake.Comments(testRepo, n)
	if len(comments) != 1 || comments[0].Body != "edited" {
		t.Fatalf("comments = %+v", comments)
	}
	want := []string{"issues.opened", "issue_comment.created", "issue_comment.edited", "issues.unlabeled", "issues.closed"}
	if got := actions(e.hook.deliveries()); !slices.Equal(got, want) {
		t.Fatalf("webhooks = %v, want %v (closing twice announces once)", got, want)
	}
	if _, err := e.fake.OpenIssue("nope/nope", alice, "x", ""); err == nil {
		t.Fatal("an unknown repository must be refused")
	}
	if err := e.fake.AddRepo("not-a-repo"); err == nil {
		t.Fatal("a malformed repository name must be refused")
	}
}

func TestForkPullRequestWebhookNamesTheForkAsTheHead(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	stranger := e.fake.Person("stranger", "NONE")
	n, err := e.fake.OpenPullRequest(testRepo, stranger, fakegithub.PullSpec{Title: "x", HeadRef: "patch", BaseRef: "main", HeadRepo: "stranger/app"})
	if err != nil || n != 1 {
		t.Fatalf("number %d, err %v", n, err)
	}
	if _, err := e.fake.OpenPullRequest(testRepo, stranger, fakegithub.PullSpec{Title: "x", BaseRef: "main"}); err == nil {
		t.Fatal("a pull request without a head must be refused")
	}
	var payload struct {
		PullRequest struct {
			Head struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	d := e.fake.Deliveries()[0]
	if err := json.Unmarshal(d.Payload, &payload); err != nil || d.Event != "pull_request" || payload.PullRequest.Head.Repo.FullName != "stranger/app" {
		t.Fatalf("payload = %s (%v)", d.Payload, err)
	}
	if err := e.fake.SetIssueState(testRepo, n, stranger, "closed"); err != nil {
		t.Fatal(err)
	}
	if got := actions(e.hook.deliveries()); !slices.Equal(got, []string{"pull_request.opened", "pull_request.closed"}) {
		t.Fatalf("webhooks = %v", got)
	}
}
