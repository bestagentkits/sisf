package fakegithub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Like GitHub, the compare API takes a branch name as well as a commit on
// either side.
func TestCompareTakesABranchNameOnEitherSide(t *testing.T) {
	t.Parallel()
	fake := New(WithToken("token"))
	if err := fake.AddRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	data := NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	base, err := fake.Commit("acme/app", map[string]string{"a.txt": "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	head, err := fake.Commit("acme/app", map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch("acme/app", "main", head); err != nil {
		t.Fatal(err)
	}
	for _, basehead := range []string{base + "...main", "main..." + head, base + "..." + head} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/repos/acme/app/compare/"+basehead, nil)
		req.Header.Set("Authorization", "Bearer token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var cmp struct{ Files []struct{ Filename string } }
		err = json.NewDecoder(resp.Body).Decode(&cmp)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("compare %s answered %d, %v", basehead, resp.StatusCode, err)
		}
		if want := basehead != "main..."+head; want != (len(cmp.Files) == 1 && cmp.Files[0].Filename == "b.txt") {
			t.Fatalf("compare %s lists %v", basehead, cmp.Files)
		}
	}
}
