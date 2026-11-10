package harness

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Upstream is one fake GitHub that several factories share: it holds the
// upstream repository and each factory's own fork of it, and fans every
// webhook delivery out to every factory, each of which accepts only the
// deliveries of its own repository.
type Upstream struct {
	GitHub  *fakegithub.Server
	GitData *fakegithub.GitData
	// Repo is the upstream repository, "owner/name".
	Repo string
	// URL is the API address.
	URL string

	secret string
	mu     sync.Mutex
	hooks  []string
}

// NewUpstream starts a fake GitHub holding the upstream repository, whose
// main branch carries the seed files.
func NewUpstream(t testing.TB) *Upstream {
	t.Helper()
	u := newHost(t)
	u.Repo = "acme/hello-app"
	must(t, u.GitHub.AddRepo(u.Repo))
	files := map[string]string{}
	for path, content := range SeedFiles {
		files[path] = content
	}
	sha, err := u.GitHub.Commit(u.Repo, files)
	must(t, err)
	must(t, u.GitData.SetBranch(u.Repo, "main", sha))
	return u
}

// newHost starts a fake GitHub with its webhook fan-out.
func newHost(t testing.TB) *Upstream {
	t.Helper()
	u := &Upstream{GitHub: fakegithub.New(), secret: token(t)}
	u.GitData = fakegithub.NewGitData(u.GitHub)
	api := httptest.NewServer(u.GitData)
	t.Cleanup(api.Close)
	u.URL = api.URL
	hub := httptest.NewServer(http.HandlerFunc(u.fanOut))
	t.Cleanup(hub.Close)
	u.GitHub.SetWebhook(hub.URL, u.secret)
	return u
}

// subscribe adds a factory's webhook route to the fan-out.
func (u *Upstream) subscribe(url string) {
	u.mu.Lock()
	u.hooks = append(u.hooks, url)
	u.mu.Unlock()
}

// fanOut sends one delivery to every subscribed factory. The answer is the
// best one any factory gave, as GitHub records one answer per delivery.
func (u *Upstream) fanOut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "unreadable delivery", http.StatusBadRequest)
		return
	}
	u.mu.Lock()
	hooks := append([]string(nil), u.hooks...)
	u.mu.Unlock()
	best := http.StatusNotFound
	for _, hook := range hooks {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, hook, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < best || best == http.StatusNotFound {
			best = resp.StatusCode
		}
	}
	w.WriteHeader(best)
}

// forkManifest is the sample manifest of a factory with its own ID that
// serves its own fork of the upstream repository.
func forkManifest(sample []byte, factory core.FactoryID) []byte {
	text := strings.Replace(string(sample), "factory_id: acme-hello", "factory_id: "+string(factory), 1)
	text = strings.Replace(text, "repository:\n  owner: acme\n", "repository:\n  owner: "+string(factory)+"\n", 1)
	return []byte(text)
}
