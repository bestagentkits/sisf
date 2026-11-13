package stages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// A candidate that moves a protected file into the application's directory
// is one renamed entry in GitHub's comparison. Read through the client the
// supervisor uses, the path the file left is a changed path too, so the
// rule of a self-published change refuses it for the Owner even when the
// stored list names only the path the file reached.
func TestAProtectedFileMovedIntoTheApplicationIsRefused(t *testing.T) {
	t.Parallel()
	const (
		base = core.CommitSHA("1111111111111111111111111111111111111111")
		head = core.CommitSHA("2222222222222222222222222222222222222222")
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/app/compare/"+string(base)+"..."+string(head) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ahead","ahead_by":1,"behind_by":0,"total_commits":1,"files":[` +
			`{"sha":"bbcd538c8e72b8c175046e27cc8f907076331401","filename":"examples/hello-app/approval.html",` +
			`"previous_filename":"ui/templates/approval.html","status":"renamed","additions":0,"deletions":0,"changes":0}]}`))
	}))
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte("token")))})
	if err != nil {
		t.Fatal(err)
	}
	stored := qa.Candidate{ChangedPaths: []string{"examples/hello-app/approval.html"}}
	candidate, err := changedPathsFromGitHub(context.Background(), Tree{GitHub: client, Repo: github.RepoRef{Owner: "acme", Name: "app"}},
		core.QAReceipt{PolicySHA: base, BaselineSHA: base, CandidateSHA: head}, stored)
	if err != nil || candidate.ChangedPathsIncomplete {
		t.Fatalf("changedPathsFromGitHub = %+v, %v", candidate, err)
	}
	err = release.CheckSelfPublishedChange(release.DefaultProtectedPaths(), release.DefaultUnitInputs(), "app", candidate.ChangedPaths)
	if !core.HasCode(err, core.CodeApprovalRequired) || !strings.Contains(err.Error(), "ui/templates/approval.html") {
		t.Fatalf("CheckSelfPublishedChange(%v) = %v; want APPROVAL_REQUIRED naming the path the file left", candidate.ChangedPaths, err)
	}
}
