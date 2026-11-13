// Package handoff is the contract between the agent zone and the trusted
// supervisor about a candidate: the patch the agent zone exports from a
// run, in the "dispatcher" namespace the control zone may read; the build
// and the candidate the supervisor makes of it, in the control zone's
// "build" namespace; and how the QA suite a candidate is judged with is
// named.
//
// It lives with the supervisor because it is part of the trusted control
// surface: a candidate cannot change what the supervisor reads from it.
package handoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Namespace is where the agent zone keeps its documents; the control zone
// and the supervisor may read it. BuildNamespace is the supervisor's, which
// the agent zone can neither read nor write.
const (
	Namespace      = "dispatcher"
	BuildNamespace = "build"
)

// maxPatchBytes bounds an exported patch.
const maxPatchBytes = 1 << 20

// PatchDoc is the patch a run's coding agent left for one evaluator
// attempt, exported by the agent zone with what the agent zone knows of the
// run. The supervisor trusts none of it beyond the patch itself: the base
// and the policy come from the policy in force, the attempt from the run's
// state record and the coder's identity from the run's dispatch record.
type PatchDoc struct {
	IssueID core.IssueRef `json:"issue_id"`
	RunID   core.RunID    `json:"run_id"`
	// Attempt is the evaluator attempt the patch is for; AgentAttempt the
	// dispatcher attempt that wrote it.
	Attempt      int    `json:"attempt"`
	AgentAttempt int    `json:"agent_attempt"`
	Diff         string `json:"diff"`
	// Digest is the artifact digest the agent backend reported.
	Digest core.Digest `json:"digest"`
	// CoderIdentity is what the exporter says wrote the patch. It is
	// informational only; see TrustedCoderIdentity.
	CoderIdentity string          `json:"coder_identity"`
	Policy        core.PolicyRefs `json:"policy"`
	// Base is the commit the run was coded on, as its brief was pinned;
	// empty means the policy revision. It is the agent zone's claim: the
	// supervisor builds on it only once it accepts it again.
	Base       core.CommitSHA `json:"base,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	ExportedAt time.Time      `json:"exported_at"`
}

// PatchKey is the key of the patch of one evaluator attempt.
func PatchKey(run core.RunID, attempt int) string { return fmt.Sprintf("patch/%s/%d", run, attempt) }

// ReadPatch reads the patch of one evaluator attempt.
func ReadPatch(ctx context.Context, kv state.KV, run core.RunID, attempt int) (PatchDoc, error) {
	var doc PatchDoc
	err := readDoc(ctx, kv, Namespace, PatchKey(run, attempt), &doc)
	if err == nil && (doc.RunID != run || doc.Attempt != attempt || len(doc.Diff) > maxPatchBytes) {
		err = core.NewError(core.CodeTampered, "the stored patch names another run or attempt, or is too large")
	}
	return doc, err
}

// CandidateDoc is a submitted candidate as the evaluator needs it.
type CandidateDoc struct {
	Candidate qa.Candidate `json:"candidate"`
	// BuildFailure says why the patch could not be built; the evaluator then
	// fails the candidate without judging it. BuildRefusal is the typed
	// reason of the refusal, for example "protected_path".
	BuildFailure string `json:"build_failure,omitempty"`
	BuildRefusal string `json:"build_refusal,omitempty"`
	// Unit is the runtime unit the candidate is a build of.
	Unit  string `json:"unit"`
	Image string `json:"image"`
	// Branch is the factory's branch that holds the candidate commit.
	Branch string `json:"branch"`
	// AgentAttempt is the dispatcher attempt whose patch was built.
	AgentAttempt int `json:"agent_attempt"`
	// Feature and Summary go into the changelog and the catalog.
	Feature     string    `json:"feature,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// BuildRecord is a finished build of one commit, written by the
// supervisor's builder, never by the agent zone: an agent's own claim of a digest proves nothing.
type BuildRecord struct {
	Commit core.CommitSHA `json:"commit"`
	Digest core.Digest    `json:"digest"`
	Unit   string         `json:"unit"`
	Image  string         `json:"image"`
	// Builder names how the build was made, for example "simulated" or "sandbox:docker".
	Builder string `json:"builder"`
	// TestsPassed is the result of the deterministic suite run with the build.
	TestsPassed   bool      `json:"tests_passed"`
	TestEvidence  string    `json:"test_evidence,omitempty"`
	SBOMRef       string    `json:"sbom_ref"`
	ProvenanceRef string    `json:"provenance_ref"`
	BuiltAt       time.Time `json:"built_at"`
}

// CandidateKey is the key of the candidate an evaluator attempt judges.
func CandidateKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("candidate/%s/%d", run, attempt)
}

// BuildKey is the key of the finished build of commit.
func BuildKey(commit core.CommitSHA) string { return "build/" + string(commit) }

// ReadCandidate reads the candidate of one evaluator attempt.
func ReadCandidate(ctx context.Context, kv state.KV, run core.RunID, attempt int) (CandidateDoc, error) {
	var doc CandidateDoc
	err := readDoc(ctx, kv, BuildNamespace, CandidateKey(run, attempt), &doc)
	if err == nil && (doc.Candidate.RunID != run || doc.Candidate.Attempt != attempt) {
		err = core.NewError(core.CodeTampered, "the stored candidate names another run or attempt")
	}
	return doc, err
}

// ReadBuild reads the finished build of commit, NOT_FOUND while there is none.
func ReadBuild(ctx context.Context, kv state.KV, commit core.CommitSHA) (BuildRecord, error) {
	var rec BuildRecord
	err := readDoc(ctx, kv, BuildNamespace, BuildKey(commit), &rec)
	if err == nil && rec.Commit != commit {
		err = core.NewError(core.CodeTampered, "the stored build names another commit")
	}
	return rec, err
}

func readDoc(ctx context.Context, kv state.KV, namespace, key string, out any) error {
	entry, err := kv.GetKV(ctx, namespace, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(entry.Value, out); err != nil {
		return core.NewError(core.CodeTampered, "the stored document "+key+" cannot be read")
	}
	return nil
}

// SuiteDigest names the QA suite in force under policy: the review criteria
// and the policy commit the evaluator judges with. The builder stamps it on
// a candidate and the supervisor derives it again from the policy in force,
// so a candidate built under another policy is never judged as current.
func SuiteDigest(policy core.PolicyRefs) core.Digest {
	sum := sha256.Sum256([]byte("sisf.qa_suite\n" + string(policy.PolicySHA) + "\n" + string(policy.ReviewSHA)))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// PutDoc writes a document of namespace; an equal one already there is kept.
func PutDoc(ctx context.Context, kv state.KV, namespace, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return core.NewError(core.CodeInternal, "a document cannot be encoded").WithCause(err)
	}
	existing, err := kv.GetKV(ctx, namespace, key)
	switch {
	case err == nil && string(existing.Value) == string(raw):
		return nil
	case err == nil:
		_, err = kv.PutKV(ctx, state.KVEntry{Namespace: namespace, Key: key, Value: raw, Version: existing.Version})
		return err
	case !core.HasCode(err, core.CodeNotFound):
		return err
	}
	_, err = kv.PutKV(ctx, state.KVEntry{Namespace: namespace, Key: key, Value: raw})
	return err
}
