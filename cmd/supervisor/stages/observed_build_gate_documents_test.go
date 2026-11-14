package stages

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const gateCommit = core.CommitSHA("1111111111111111111111111111111111111111")

// observedBuildOf runs the observed-build gate over rec, stored as the
// build of gateCommit, for a candidate of digest.
func observedBuildOf(t *testing.T, db *sqlstoretest.Database, rec handoff.BuildRecord, digest core.Digest) (bool, string) {
	t.Helper()
	ctx := context.Background()
	store := db.Open(t, core.ZoneControl)
	if err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(gateCommit), rec)
	}); err != nil {
		t.Fatal(err)
	}
	passed, detail, err := GateSources{State: store}.observedBuild(ctx, qa.Candidate{CommitSHA: gateCommit, Digest: digest})
	if err != nil {
		t.Fatalf("the observed-build gate: %v", err)
	}
	return passed, detail
}

// The observed-build gate passes a build only on the SBOM and provenance
// the supervisor holds for it. References that name no document it holds,
// whatever they say, are a claim, and fail the gate.
func TestABuildWhoseSupplyChainRefsNameNoHeldDocumentFailsTheObservedBuildGate(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		rec := handoff.BuildRecord{Commit: gateCommit, Digest: reportedDigest, Unit: "app", Image: "registry.local/app@" + string(reportedDigest),
			Builder: "sandbox:docker", TestsPassed: true, SBOMRef: "sbom:invented", ProvenanceRef: "provenance:invented"}
		if passed, detail := observedBuildOf(t, db, rec, reportedDigest); passed {
			t.Fatalf("a build whose SBOM and provenance are bare strings passed the observed-build gate: %s", detail)
		}
	})
}

// A build the supervisor recorded with the documents it made passes the
// gate once stored and read back, for its own candidate and no other.
func TestABuildRecordedWithTheSupervisorsDocumentsPassesTheObservedBuildGate(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		repo := &recordingRepo{files: map[string]string{"examples/hello-app/main.go": "a\n"}}
		b := newTestBuilder(repo)
		b.Images = SimulatedImages{}
		built, err := b.Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 1, Base: baseSHA,
			Diff: "--- a/examples/hello-app/main.go\n+++ b/examples/hello-app/main.go\n@@ -1 +1 @@\n-a\n+b\n"})
		if err != nil {
			t.Fatalf("the build: %v", err)
		}
		rec := built.Record
		rec.Commit = gateCommit
		if passed, detail := observedBuildOf(t, db, rec, rec.Digest); passed {
			t.Fatalf("documents of another commit passed the gate: %s", detail)
		}
		rec = built.Record
		docs := supplychain.BuildDocuments{SBOM: rec.SBOM, SBOMRef: rec.SBOMRef, Provenance: rec.Provenance, ProvenanceRef: rec.ProvenanceRef}
		if err := supplychain.CheckBuildDocuments(rec.Digest, rec.Commit, docs); err != nil {
			t.Fatalf("the documents the builder recorded: %v", err)
		}
		ctx := context.Background()
		store := db.Open(t, core.ZoneControl)
		if err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(rec.Commit), rec)
		}); err != nil {
			t.Fatal(err)
		}
		gates := GateSources{State: store}
		passed, detail, err := gates.observedBuild(ctx, qa.Candidate{CommitSHA: rec.Commit, Digest: rec.Digest})
		if err != nil || !passed {
			t.Fatalf("the supervisor's own build failed the gate once stored: %s, %v", detail, err)
		}
		// The simulation builds no program, so its SBOM lists no module, and
		// the gate's detail, which the receipt records, says so.
		if !strings.Contains(detail, "modules are unknown") {
			t.Fatalf("the gate passed a build whose modules are unknown without saying so: %q", detail)
		}
		if passed, _, err := gates.observedBuild(ctx, qa.Candidate{CommitSHA: rec.Commit, Digest: reportedDigest}); err != nil || passed {
			t.Fatalf("the build passed the gate for another candidate's digest: %v", err)
		}
	})
}
