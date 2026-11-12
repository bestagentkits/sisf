package policy_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Commits used by the tests. revA is the revision the Owner approved; revB
// and revC are later commits nobody approved yet.
const (
	revA core.CommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	revB core.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	revC core.CommitSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

// factory is the factory the foundation files of this repository declare.
const factory core.FactoryID = "acme-hello"

var testNow = time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)

// repository is a policy.Source over commits held in memory. It counts the
// reads of each commit, so a test can show that an unapproved commit was
// never even opened.
type repository struct {
	mu      sync.Mutex
	commits map[core.CommitSHA]map[string]string
	reads   map[core.CommitSHA]int
	down    bool
}

func (r *repository) ReadFile(_ context.Context, revision core.CommitSHA, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down {
		return nil, core.NewError(core.CodeUnavailable, "the repository cannot be reached")
	}
	r.reads[revision]++
	content, ok := r.commits[revision][path]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no such file at this commit")
	}
	return []byte(content), nil
}

// commit stores a copy of the approved files, changed by edit, as revision.
func (r *repository) commit(revision core.CommitSHA, edit func(files map[string]string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := maps.Clone(r.commits[revA])
	if edit != nil {
		edit(files)
	}
	r.commits[revision] = files
}

func (r *repository) readsOf(revision core.CommitSHA) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads[revision]
}

// repositoryFiles reads the foundation files of this repository, the real
// files the loader is written for.
func repositoryFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, path := range []string{policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures, policy.PathFactory} {
		content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("foundation file %s cannot be read: %v", path, err)
		}
		files[path] = string(content)
	}
	return files
}

// world is a factory's policy machinery over in-memory state: a repository,
// the approved baseline, the approval store and the loader.
type world struct {
	repo      *repository
	state     *intaketest.State
	clock     *intaketest.Clock
	audit     *intaketest.AuditLog
	approvals *approvals.KVStore
	baselines *policy.Baselines
	loader    *policy.Loader
}

// newWorld builds a factory whose repository holds the real foundation files
// at revA. Nothing is approved yet.
func newWorld(t *testing.T) *world {
	t.Helper()
	clock := intaketest.NewClock(testNow)
	keys := intaketest.NewKeys("approval-key-1")
	w := &world{
		repo:  &repository{commits: map[core.CommitSHA]map[string]string{revA: repositoryFiles(t)}, reads: map[core.CommitSHA]int{}},
		state: intaketest.NewState(clock), clock: clock, audit: &intaketest.AuditLog{},
	}
	var err error
	w.approvals, err = approvals.NewStore(approvals.Config{State: w.state, FactoryID: factory, Signer: keys, Verifier: keys, Audit: w.audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	w.baselines, err = policy.NewBaselines(policy.BaselinesConfig{
		State: w.state, Source: w.repo, Approvals: w.approvals, Audit: w.audit, Clock: clock, FactoryID: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.loader, err = policy.NewLoader(w.repo, w.baselines)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// approved builds a factory whose Owner approved revA at install.
func approved(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	if _, err := w.baselines.Approve(context.Background(), w.owner(), revA, ""); err != nil {
		t.Fatalf("the Owner could not approve the first policy revision: %v", err)
	}
	return w
}

// owner is the person who owns the factory, identity just confirmed.
func (w *world) owner() core.Actor {
	return core.Actor{ID: "user:owner-1", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceCLI, StepUpAt: w.clock.Now()}
}

// requestChange files the approval request for making revision the policy
// in force and returns the submission an approver would send.
func (w *world) requestChange(t *testing.T, id string, revision core.CommitSHA) approvals.Submission {
	t.Helper()
	return w.request(t, w.approvals, id, revision, nil)
}

// request files, in store, an approval request about the content of
// revision under the policy in force. change, when given, turns it into a
// request for something other than making that revision the policy.
func (w *world) request(t *testing.T, store *approvals.KVStore, id string, revision core.CommitSHA, change func(*approvals.Request)) approvals.Submission {
	t.Helper()
	ctx := context.Background()
	digest, err := w.baselines.Digest(ctx, revision)
	if err != nil {
		t.Fatalf("the candidate revision has no digest: %v", err)
	}
	current, err := w.baselines.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := approvals.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	request := approvals.Request{
		ID: id, FactoryID: factory, Action: "policy.approve_revision", SubjectDigest: digest,
		PolicyRevision: current.Revision, Nonce: nonce, ExpiresAt: w.clock.Now().Add(24 * time.Hour),
		RequestedBy: "service:control", RiskSummary: "Replaces the decision rules the factory follows.",
		StepUp: true, Status: approvals.StatusPending, CreatedAt: w.clock.Now(),
	}
	if change != nil {
		change(&request)
	}
	if _, err := store.Create(ctx, request); err != nil {
		t.Fatal(err)
	}
	return approvals.Submission{RequestID: id, Nonce: nonce, SubjectDigest: digest,
		PolicyRevision: current.Revision, Decision: approvals.DecisionApprove}
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

// inForce returns the revision the loader serves, failing the test on a hold.
func (w *world) inForce(t *testing.T) *policy.Snapshot {
	t.Helper()
	snapshot, err := w.loader.Current(context.Background())
	if err != nil {
		t.Fatalf("no policy is in force: %v", err)
	}
	return snapshot
}
