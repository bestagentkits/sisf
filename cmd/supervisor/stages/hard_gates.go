package stages

import (
	"context"
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// gate is a deterministic hard gate built from a function.
type gate struct {
	name  qa.GateName
	check func(ctx context.Context, c qa.Candidate) (bool, string, error)
}

func (g gate) Name() qa.GateName { return g.name }

func (g gate) Check(ctx context.Context, c qa.Candidate) (qa.GateResult, error) {
	passed, detail, err := g.check(ctx, c)
	if err != nil {
		return qa.GateResult{}, err
	}
	return qa.GateResult{Gate: g.name, Passed: passed, Detail: detail}, nil
}

// FileReader reads a file of the factory's repository at a commit.
type FileReader interface {
	ReadFile(ctx context.Context, commit core.CommitSHA, path string) ([]byte, error)
}

// GateSources are what the deterministic hard gates read. Each reads only
// what the trusted side recorded or the repository holds at the commit.
type GateSources struct {
	State    state.Store
	Files    FileReader
	Redactor *redact.Redactor
	LKG      *release.LKGBook
	Backend  deploy.Backend
	// SupplyChain is the supply-chain gate (see supplychain.NewGate).
	SupplyChain qa.HardGate
}

// HardGates returns every hard gate but the evaluator's own independence.
func HardGates(src GateSources) []qa.HardGate {
	return []qa.HardGate{
		gate{qa.GateTestsContract, src.testsContract},
		gate{qa.GatePermissions, protectedGate("changes CI, code ownership or the factory's policy, which needs the Owner",
			ProtectedCI, ProtectedOwnership, ProtectedPolicy)},
		gate{qa.GateMigrations, protectedGate("changes a schema migration, which needs the Owner's approval and a backup", ProtectedMigration)},
		gate{qa.GateSecrets, src.secrets},
		gate{qa.GateObservedBuild, src.observedBuild},
		gate{qa.GateResourceBudget, src.resourceBudget},
		gate{qa.GateRollbackReadiness, src.rollbackReadiness},
		src.SupplyChain,
	}
}

func (src GateSources) build(ctx context.Context, commit core.CommitSHA) (handoff.BuildRecord, error) {
	var rec handoff.BuildRecord
	err := src.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		rec, err = handoff.ReadBuild(ctx, tx, commit)
		return err
	})
	return rec, err
}

// testsContract passes when the suite run with the build passed.
func (src GateSources) testsContract(ctx context.Context, c qa.Candidate) (bool, string, error) {
	rec, err := src.build(ctx, c.CommitSHA)
	if err != nil {
		return false, "", err
	}
	return rec.TestsPassed, rec.TestEvidence, nil
}

// observedBuild passes when the build ledger holds this exact build, with
// the SBOM and provenance the supervisor made of it: each reference the
// digest of a document the record holds, describing this program and this
// commit (supplychain.CheckBuildDocuments). A reference that names no
// document held with the build is a claim, and fails the gate. An SBOM that
// lists no module passes, since units need not be Go programs, and the
// detail says the modules are unknown.
func (src GateSources) observedBuild(ctx context.Context, c qa.Candidate) (bool, string, error) {
	rec, err := src.build(ctx, c.CommitSHA)
	if err != nil {
		return false, "", err
	}
	if rec.Digest != c.Digest {
		return false, "the recorded build is not this candidate's", nil
	}
	if err := supplychain.CheckBuildDocuments(rec.Digest, rec.Commit, supplychain.BuildDocuments{
		SBOM: rec.SBOM, SBOMRef: rec.SBOMRef, Provenance: rec.Provenance, ProvenanceRef: rec.ProvenanceRef,
	}); err != nil {
		return false, "the recorded build has no SBOM and provenance the supervisor made of it: " + err.Error(), nil
	}
	// The detail goes into the QA receipt, so a passed gate over an SBOM
	// that lists no module says the modules are unknown, and why.
	if why, unknown := supplychain.ModulesUnknown(rec.SBOM); unknown {
		return true, "built by " + rec.Builder + "; its modules are unknown: " + why, nil
	}
	return true, "built by " + rec.Builder + "; its modules are read from the program's Go build information", nil
}

// secrets passes when no changed file holds a credential.
func (src GateSources) secrets(ctx context.Context, c qa.Candidate) (bool, string, error) {
	for _, p := range c.ChangedPaths {
		content, err := src.Files.ReadFile(ctx, c.CommitSHA, p)
		if core.HasCode(err, core.CodeNotFound) {
			continue
		}
		if err != nil {
			return false, "", err
		}
		if text := string(content); src.Redactor.Secrets(text) != text {
			return false, "a credential was found in " + p, nil
		}
	}
	return true, fmt.Sprintf("%d changed files hold no credential", len(c.ChangedPaths)), nil
}

// resourceBudget passes when the budget gate admitted the run.
func (src GateSources) resourceBudget(ctx context.Context, c qa.Candidate) (bool, string, error) {
	var reservation string
	err := src.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(c.RunID))
		if err != nil {
			return err
		}
		reservation = reservationOf(entry.Value)
		return nil
	})
	if err != nil {
		return false, "", err
	}
	return reservation != "", "the run was admitted under reservation " + reservation, nil
}

// rollbackReadiness passes when the unit has a last known good release and
// the deployment can observe and undo a release.
func (src GateSources) rollbackReadiness(ctx context.Context, c qa.Candidate) (bool, string, error) {
	unit, err := src.unitOf(ctx, c)
	if err != nil {
		return false, "", err
	}
	if _, found, err := src.LKG.Get(ctx, unit); err != nil || !found {
		return false, "unit " + unit + " has no last known good release to return to", err
	}
	caps, err := src.Backend.Capabilities(ctx)
	if err != nil {
		return false, "", err
	}
	if !deploy.SelfPublishAllowed(caps) {
		return false, "the " + src.Backend.Name() + " backend cannot observe and roll back a release", nil
	}
	return true, "unit " + unit + " can return to its last known good release", nil
}

func (src GateSources) unitOf(ctx context.Context, c qa.Candidate) (string, error) {
	var doc handoff.CandidateDoc
	err := src.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		doc, err = handoff.ReadCandidate(ctx, tx, c.RunID, c.Attempt)
		return err
	})
	return doc.Unit, err
}

// protectedGate fails a change to any path ProtectedPath puts in one of
// kinds. The builder refuses such a patch before it is committed; the gate
// holds the same line for every candidate the evaluator sees.
func protectedGate(why string, kinds ...ProtectedKind) func(context.Context, qa.Candidate) (bool, string, error) {
	return func(_ context.Context, c qa.Candidate) (bool, string, error) {
		for _, p := range c.ChangedPaths {
			if kind, ok := ProtectedPath(p); ok && slices.Contains(kinds, kind) {
				return false, p + " " + why, nil
			}
		}
		return true, "no change of this kind", nil
	}
}
