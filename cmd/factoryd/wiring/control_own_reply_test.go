package wiring_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The factory's reply to a person's comment is its own edit like any other:
// polling does not bring it back as a change for intake, although the
// person's comment it answers was made just before it.
func TestPollingDoesNotBringBackTheFactorysReplyToAComment(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	// The first pass polls the new issue, the second asks on it.
	f.Pass()
	f.Pass()
	before := len(factoryComments(t, f, number))
	f.Comment(number, "maintainer", "MEMBER", "It also fails at night.")
	f.Clock.Advance(wiring.DefaultReconcileInterval)
	// This pass polls the comment, the next replies to it.
	f.Pass()
	if got := reconciledIssues(t, f); !got[number] {
		t.Fatalf("the person's comment was not polled: %v", got)
	}
	f.Pass()
	if replies := len(factoryComments(t, f, number)) - before; replies != 1 {
		t.Fatalf("the factory replied %d times to the comment", replies)
	}
	f.Clock.Advance(wiring.DefaultReconcileInterval)
	if err := f.Control.ReconcileGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := reconciledIssues(t, f); got[number] {
		t.Fatal("polling brought back the factory's reply to a comment as a change")
	}
}

// factoryComments are the comments of issue number the factory wrote.
func factoryComments(t *testing.T, f *harness.Factory, number int) []string {
	t.Helper()
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		if c.Author.Login == f.GitHub.Bot().Login {
			out = append(out, c.Body)
		}
	}
	return out
}
