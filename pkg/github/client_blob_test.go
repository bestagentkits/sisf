package github_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// A blob is read by the name its tree gives it, and only content that is
// that blob is returned.
func TestReadBlobReturnsOnlyTheNamedBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake, _, client := gitDataClient(t)
	sha, err := fake.Commit(testRepo, map[string]string{"plugins/pay/pay.php": "<?php // pay"})
	if err != nil {
		t.Fatal(err)
	}
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	entries, err := client.ListTree(ctx, repo, core.CommitSHA(sha))
	if err != nil {
		t.Fatal(err)
	}
	var blob string
	for _, e := range entries {
		if e.Path == "plugins/pay/pay.php" {
			blob = e.SHA
		}
	}
	if got, err := client.ReadBlob(ctx, repo, blob); err != nil || string(got) != "<?php // pay" {
		t.Fatalf("ReadBlob = %q, %v", got, err)
	}
	if _, err := client.ReadBlob(ctx, repo, "../contents"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a bad name = %v, want INVALID_ARGUMENT", err)
	}

	// A server that answers other content under the name is not believed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"encoding":"base64","size":5,"content":"` + base64.StdEncoding.EncodeToString([]byte("other")) + `"}`))
	}))
	t.Cleanup(srv.Close)
	liar, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte(testToken)))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := liar.ReadBlob(ctx, repo, blob); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("other content under the name = %v, want UNAVAILABLE", err)
	}
}
