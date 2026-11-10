package wiring

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const testFactory core.FactoryID = "wiring-test"

func openTestStore(t *testing.T, path string, zone core.Zone) (*sqlstore.Store, error) {
	t.Helper()
	clock := sqlstoretest.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store, err := OpenStore(context.Background(), core.NewSecretValue([]byte(path)), testFactory, zone, clock)
	if err == nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	return store, err
}

func TestOnlyTheControlZoneMayPrepareTheStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "factory.db")
	if _, err := openTestStore(t, path, core.ZoneAgent); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("the agent zone opened an unprepared store: %v", err)
	}
	if _, err := openTestStore(t, path, core.ZoneControl); err != nil {
		t.Fatalf("the control zone could not prepare the store: %v", err)
	}
	if _, err := openTestStore(t, path, core.ZoneAgent); err != nil {
		t.Fatalf("the agent zone could not open the prepared store: %v", err)
	}
}

func sha(c string) core.CommitSHA { return core.CommitSHA(strings.Repeat(c, 40)) }

func TestPublishedPolicyRefsAreCheckedBeforeARunUsesThem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"), core.ZoneControl)
	if err != nil {
		t.Fatal(err)
	}
	read := func(rev core.CommitSHA) error {
		return store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := readPolicyRefs(ctx, tx, rev)
			return err
		})
	}
	put := func(rev core.CommitSHA, value string) {
		t.Helper()
		if err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: policyRefsNamespace, Key: policyRefsPrefix + string(rev), Value: []byte(value)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := read(sha("a")); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("an unpublished revision did not hold: %v", err)
	}
	put(sha("b"), "{not json")
	if err := read(sha("b")); !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("unreadable references were used: %v", err)
	}
	put(sha("c"), `{"policy_sha":"`+string(sha("d"))+`"}`)
	if err := read(sha("c")); !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("references of another revision were used: %v", err)
	}
}

func TestABriefCarriesTheApprovedScopeAndItsPolicy(t *testing.T) {
	t.Parallel()
	issue := core.IssueRef{Owner: "acme", Repo: "shop", Number: 7}
	scope := intake.SoW{Issue: issue, Version: 1, Title: "Make the health check fast", DoD: []string{"answers in 1s", "has a test"}}
	refs := core.PolicyRefs{PolicySHA: sha("a")}
	brief := BriefOf(scope, refs)
	if brief.Policy != refs || !strings.Contains(brief.Handoff.DoD, "- answers in 1s\n- has a test") {
		t.Fatalf("brief = %+v", brief)
	}
}

type countingReader struct{ calls int }

func (c *countingReader) ReadFile(context.Context, github.RepoRef, core.CommitSHA, string) ([]byte, error) {
	c.calls++
	return []byte("content"), nil
}

func TestThePolicySourceReadsACommittedFileOnce(t *testing.T) {
	t.Parallel()
	if _, err := NewGitHubPolicySource(nil, github.RepoRef{Owner: "acme", Name: "shop"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a source without a reader was built: %v", err)
	}
	files := &countingReader{}
	src, err := NewGitHubPolicySource(files, github.RepoRef{Owner: "acme", Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, err := src.ReadFile(context.Background(), sha("a"), "AGENTS.md")
		if err != nil || string(got) != "content" {
			t.Fatalf("read = %q, %v", got, err)
		}
		got[0] = 'X' // a caller's change does not reach the cache
	}
	if files.calls != 1 {
		t.Fatalf("GitHub was read %d times for one committed file", files.calls)
	}
}
