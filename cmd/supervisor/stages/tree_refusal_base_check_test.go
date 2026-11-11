package stages

import (
	"context"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// vanishedBase is the factory's repository after its base commit went
// away: everything works as on GitHub except that the base can no longer
// be found.
type vanishedBase struct{ *github.RESTClient }

func (v vanishedBase) CommitExists(ctx context.Context, repo github.RepoRef, _ core.CommitSHA) error {
	return v.RESTClient.CommitExists(ctx, repo, "ffffffffffffffffffffffffffffffffffffffff")
}

// GitHub answers 422 on the tree both for paths it will not hold and for a
// base tree that is gone. Only the first is the patch's fault: the base is
// read before the refusal is charged to it.
func TestATreeRefusedOverAVanishedBaseIsNotChargedToThePatch(t *testing.T) {
	t.Parallel()
	g := newRealGitHub(t)
	g.data.FailOn(http.MethodPost, "/git/trees", 422, "Unprocessable Entity")
	b := &CandidateBuilder{FactoryID: "f1", Repo: github.RepoRef{Owner: "acme", Name: "app"}, Git: vanishedBase{g.client},
		Images: SimulatedImages{}, Units: DefaultUnitRouter(), Clock: core.SystemClock{}}
	_, err := b.Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 1, Base: g.base, Diff: editMain})
	if r, refused := RefusalOf(err); refused {
		t.Fatalf("a tree refused over a vanished base was charged to the patch as %s", r.Reason)
	}
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the vanished base is not reported as the outage it is: %v", err)
	}

	// With the base still there, the same refusal is the patch's.
	g = newRealGitHub(t)
	g.data.FailOn(http.MethodPost, "/git/trees", 422, "Unprocessable Entity")
	_, err = g.build(t, editMain)
	if r, refused := RefusalOf(err); !refused || r.Reason != RefusalPathRejected {
		t.Fatalf("a tree refused with the base readable: %v", err)
	}
}
