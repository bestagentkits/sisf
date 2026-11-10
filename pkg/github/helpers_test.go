package github_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const (
	testRepo       = "acme/app"
	testToken      = "fake-installation-token-value"
	testSecret     = "fake-webhook-secret"
	testInstall    = int64(4242)
	factoryLogin   = "sisf-factory[bot]"
	factoryAccount = int64(900001)
)

var repoRef = github.RepoRef{Owner: "acme", Name: "app"}

var self = github.Identity{ID: factoryAccount, Login: factoryLogin}

// fakeClock is a core.Clock that never sleeps: After records the wait, moves
// the clock forward by it and fires at once. With block set, After never
// fires, which lets a test cancel a context while a wait is pending.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
	block bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	if !c.block {
		c.now = c.now.Add(d)
		ch <- c.now
	}
	return ch
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

var _ core.Clock = (*fakeClock)(nil)

// factory is a fake GitHub with one repository and a client talking to it.
type factory struct {
	fake   *fakegithub.Server
	srv    *httptest.Server
	client *github.RESTClient
	clock  *fakeClock
}

func newFactory(t *testing.T, tweak ...func(*github.ClientConfig)) *factory {
	t.Helper()
	fake := fakegithub.New(fakegithub.WithToken(testToken), fakegithub.WithBot(factoryLogin, factoryAccount))
	if err := fake.AddRepo(testRepo); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	clock := newFakeClock()
	cfg := github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte(testToken))), Clock: clock}
	for _, f := range tweak {
		f(&cfg)
	}
	client, err := github.NewRESTClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &factory{fake: fake, srv: srv, client: client, clock: clock}
}

func (f *factory) person(login string) fakegithub.User { return f.fake.Person(login, "NONE") }

func mustOpen(t *testing.T, f *factory, by fakegithub.User, title string, labels ...string) int {
	t.Helper()
	n, err := f.fake.OpenIssue(testRepo, by, title, "", labels...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var bg = context.Background()

func fakePull() fakegithub.PullSpec {
	return fakegithub.PullSpec{Title: "p", HeadRef: "topic", BaseRef: "main"}
}

var zeroTime time.Time
