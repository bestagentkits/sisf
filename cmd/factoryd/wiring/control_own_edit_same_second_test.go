package wiring_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/github"
)

// GitHub's update times have whole-second precision, so a person's change
// in the same second as an edit of the factory leaves the issue at the
// update time the edit left it. On an install without webhooks that change
// is still taken in: it is not mistaken for the factory's own edit.
func TestAPersonsChangeInTheSecondOfAFactoryEditIsNotTakenForIt(t *testing.T) {
	t.Parallel()
	changes := map[string]func(f *harness.Factory, number int) error{
		"comment": func(f *harness.Factory, number int) error {
			_, err := f.GitHub.AddComment(f.Repo, number, f.GitHub.Person("maintainer", "MEMBER"), "It also fails at night.")
			return err
		},
		"label": func(f *harness.Factory, number int) error {
			return f.GitHub.AddLabels(f.Repo, number, f.GitHub.Person("maintainer", "MEMBER"), github.LabelKindFeature)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := harness.New(t, harness.Options{OperatorToken: true})
			number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
			// The first pass polls the new issue, the second asks on it.
			f.Pass()
			f.Pass()
			if asked := countComments(t, f, number, "More information needed"); asked != 1 {
				t.Fatalf("the question was asked %d times", asked)
			}
			// The person's change lands in the second of the question.
			f.GitHub.HoldClock(true)
			if err := change(f, number); err != nil {
				t.Fatal(err)
			}
			f.GitHub.HoldClock(false)
			f.Clock.Advance(wiring.DefaultReconcileInterval)
			if err := f.Control.ReconcileGitHub(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := reconciledIssues(t, f); !got[number] {
				t.Fatalf("the person's %s in the second of the factory's edit was taken for that edit", name)
			}
		})
	}
}
