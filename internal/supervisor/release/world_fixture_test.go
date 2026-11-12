package release

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// fakeFreeze is the emergency stop as the pipeline sees it.
type fakeFreeze struct{ frozen atomic.Bool }

func (f *fakeFreeze) Frozen(context.Context) (bool, error) { return f.frozen.Load(), nil }

// fakePublisher records what it was asked to publish.
type fakePublisher struct {
	mu      sync.Mutex
	entries []catalog.Entry
	err     error
	// hook, when set, is called on every Publish, before it answers.
	hook func()
}

func (p *fakePublisher) Publish(_ context.Context, e catalog.Entry) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hook != nil {
		p.hook()
	}
	if p.err != nil {
		return "", p.err
	}
	p.entries = append(p.entries, e)
	return "acme/hello#pull/1", nil
}

// fakeSynthetic answers every synthetic run with pass.
type fakeSynthetic struct {
	pass bool
	runs atomic.Int32
}

func (s *fakeSynthetic) Run(context.Context, deploy.Revision) (SyntheticResult, error) {
	s.runs.Add(1)
	return SyntheticResult{Passed: s.pass, EvidenceRef: "synthetic:run"}, nil
}

// storeBlocks keeps the record of each block as a document of its own
// namespace, in the transaction that blocks the issue, as the supervisor's
// recorder does.
type storeBlocks struct{}

const blocksNamespace = "release-blocks"

func (storeBlocks) RecordBlock(ctx context.Context, kv state.KV, b Block) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = kv.PutKV(ctx, state.KVEntry{Namespace: blocksNamespace, Key: string(b.RunID), Value: raw})
	return err
}

// block reads the record of why the pipeline blocked the issue of run.
func (w *world) block(run core.RunID) (Block, bool) {
	w.t.Helper()
	var b Block
	found := false
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, blocksNamespace, string(run))
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return json.Unmarshal(entry.Value, &b)
	})
	if err != nil {
		w.t.Fatalf("read the block of %s: %v", run, err)
	}
	return b, found
}

// world is one factory as the release pipeline sees it: a real operational
// store, a trust volume whose LKG is the root-signed release manifest, a
// deployment backend serving the LKG, and the supervisor's keys.
type world struct {
	t         *testing.T
	clock     *fakeClock
	keys      *keyring
	store     *sqlstore.Store
	backend   *fakeBackend
	trustDir  string
	book      *LKGBook
	freeze    *fakeFreeze
	publisher *fakePublisher
	commits   *fakeCommits
	cfg       Config
}

func newWorld(t *testing.T) *world {
	t.Helper()
	clock := &fakeClock{now: startTime}
	w := &world{t: t, clock: clock, keys: newKeyring(t, clock), freeze: &fakeFreeze{}, publisher: &fakePublisher{}, commits: &fakeCommits{}}
	w.store = openStore(t, clock)
	w.backend = newFakeBackend(clock, lkgDigest)
	w.trustDir = filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(w.trustDir, 0o700); err != nil {
		t.Fatalf("trust volume: %v", err)
	}
	w.book = w.recordInstallLKG(lkgDigest)
	w.cfg = Config{
		FactoryID: testFactory, State: w.store, Backend: w.backend, Verifier: w.keys.verifier, VerifierAt: w.keys.verifierAt, LKG: w.book,
		Freeze: w.freeze, Publisher: w.publisher, Blocks: storeBlocks{}, Clock: clock, Commits: w.commits,
	}
	return w
}

// openStore opens a migrated SQLite store for the control zone.
func openStore(t *testing.T, clock core.Clock) *sqlstore.Store {
	t.Helper()
	ctx := context.Background()
	store, err := sqlstore.Open(ctx, sqlstore.Config{
		Dialect: sqlstore.DialectSQLite, DSN: filepath.Join(t.TempDir(), "state.db"),
		FactoryID: testFactory, Zone: core.ZoneControl, Clock: clock,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx, state.MigrateOptions{}); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	return store
}

// recordInstallLKG writes the root-signed release manifest record that pins
// digest as the unit's release, and opens the LKG book on it.
func (w *world) recordInstallLKG(digest core.ReleaseDigest) *LKGBook {
	w.t.Helper()
	ctx := context.Background()
	verifier, err := signing.NewVerifier(w.keys.rootPub)
	if err != nil {
		w.t.Fatalf("root verifier: %v", err)
	}
	store, err := supervisor.OpenLKGStore(w.trustDir, filepath.Join(w.trustDir, supervisor.DefaultLKGFile), testFactory, verifier)
	if err != nil {
		w.t.Fatalf("open LKG store: %v", err)
	}
	signed, err := signing.SignManifest(ctx, w.keys.root, signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion, FactoryID: testFactory, IssuedAt: startTime,
		Units:                map[manifest.UnitName]core.Digest{manifest.UnitName(testUnit): core.Digest(digest), manifest.UnitSupervisor: suiteDigest},
		SupervisorExecutable: suiteDigest,
	})
	if err != nil {
		w.t.Fatalf("sign release manifest: %v", err)
	}
	if err := store.Save(ctx, supervisor.LKGRecord{Release: signed, RecordedAt: startTime}); err != nil {
		w.t.Fatalf("save LKG record: %v", err)
	}
	book, err := OpenLKGBook(w.trustDir, testFactory, store, w.clock)
	if err != nil {
		w.t.Fatalf("open LKG book: %v", err)
	}
	return book
}

// pipeline builds the pipeline from the world's configuration.
func (w *world) pipeline() *Pipeline {
	w.t.Helper()
	p, err := New(w.cfg)
	if err != nil {
		w.t.Fatalf("New: %v", err)
	}
	return p
}

// request is a valid release request of build for run on issue.
func (w *world) request(issue core.IssueRef, run core.RunID, build core.Digest) Request {
	c := candidate(issue, run, build)
	return Request{
		IssueID: issue, RunID: run, Candidate: c, Receipt: w.keys.receiptFor(w.t, c, nil),
		Artifact: w.keys.artifactFor(w.t, build), Feature: "Release with observed revision and automatic rollback",
		Summary: "Show the revision on the hello page",
	}
}

// record reads the stored lifecycle record of issue.
func (w *world) record(issue core.IssueRef) core.StateRecord {
	w.t.Helper()
	var record core.StateRecord
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	})
	if err != nil {
		w.t.Fatalf("read record %s: %v", issue, err)
	}
	return record
}

// lkg reads the LKG of the unit.
func (w *world) lkg() LKG {
	w.t.Helper()
	entry, found, err := w.book.Get(context.Background(), testUnit)
	if err != nil || !found {
		w.t.Fatalf("read LKG: found=%v err=%v", found, err)
	}
	return entry
}

// recoveryReport reads the stored recovery report of run.
func (w *world) recoveryReport(run core.RunID) (RecoveryReport, bool) {
	w.t.Helper()
	var report RecoveryReport
	found := false
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, RecoveryNamespace, RecoveryKey(run))
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return json.Unmarshal(entry.Value, &report)
	})
	if err != nil {
		w.t.Fatalf("read recovery report: %v", err)
	}
	return report, found
}
