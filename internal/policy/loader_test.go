package policy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheFoundationFilesOfThisRepositoryLoadAtTheApprovedCommit(t *testing.T) {
	t.Parallel()
	w := approved(t)
	s := w.inForce(t)
	if s.Revision != revA || s.Digest.Validate() != nil {
		t.Fatalf("snapshot is not pinned: revision %s, digest %s", s.Revision, s.Digest)
	}
	if s.Manifest == nil || s.Manifest.FactoryID != "acme-hello" || s.Manifest.Release.Policy != core.DelegationProposal {
		t.Fatalf("manifest = %+v", s.Manifest)
	}
	if s.DecisionSchemaVersion != 1 {
		t.Fatalf("decision schema version = %d", s.DecisionSchemaVersion)
	}
	for _, name := range []string{"Decision Layer", "decision layer", "  Trusted   release supervisor "} {
		if !s.Catalog.Has(name) {
			t.Errorf("the catalogue does not list %q", name)
		}
	}
	if len(s.Catalog.Features) != 9 || s.Catalog.Has("Marketplace") || s.Catalog.Has("") {
		t.Fatalf("catalogue = %+v", s.Catalog.Features)
	}
	for _, doc := range []policy.Document{s.Agents, s.Review, s.Decisions} {
		if doc.Content == "" || doc.Digest.Validate() != nil {
			t.Errorf("%s was not loaded with a digest", doc.Path)
		}
	}
	refs, err := s.Refs(core.StepIntake)
	if err != nil || refs.Validate() != nil || refs.ModelRoute.Model != "small" {
		t.Fatalf("Refs = %+v, %v", refs, err)
	}
	if _, err := s.Refs("thinking"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a step without a configured model route must be refused, got %v", err)
	}
}

func TestNoPolicyIsInForceBeforeTheOwnerApprovesOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, err := w.loader.Current(context.Background())
	wantCode(t, err, core.CodePolicyHold, "policy before any approval")
	_, err = w.loader.At(context.Background(), revA)
	wantCode(t, err, core.CodePolicyHold, "a commit nobody approved")
	if n := w.repo.readsOf(revA); n != 0 {
		t.Fatalf("an unapproved commit was read %d times", n)
	}
}

func TestOnlyAFullCommitNamesAPolicyRevision(t *testing.T) {
	t.Parallel()
	w := approved(t)
	for _, ref := range []core.CommitSHA{"main", "HEAD", "aaaaaaa", ""} {
		_, err := w.loader.At(context.Background(), ref)
		wantCode(t, err, core.CodePolicyHold, "policy at "+string(ref))
	}
}

// brokenAt approves revA, then makes revB the baseline candidate after edit
// and returns the error of approving it.
func brokenAt(t *testing.T, edit func(files map[string]string)) error {
	t.Helper()
	w := newWorld(t)
	w.repo.commit(revB, edit)
	_, err := w.baselines.Approve(context.Background(), w.owner(), revB, "")
	if err == nil {
		t.Fatal("a broken policy revision was approved")
	}
	if _, current := w.baselines.Current(context.Background()); !core.HasCode(current, core.CodeNotFound) {
		t.Fatalf("a broken policy revision became the baseline: %v", current)
	}
	return err
}

func TestAnIncompleteOrContradictoryPolicySetIsAHold(t *testing.T) {
	t.Parallel()
	cases := map[string]func(files map[string]string){
		"AGENTS.md is missing":     func(f map[string]string) { delete(f, policy.PathAgents) },
		"REVIEW.md is missing":     func(f map[string]string) { delete(f, policy.PathReview) },
		"DECISIONS.md is missing":  func(f map[string]string) { delete(f, policy.PathDecisions) },
		"the manifest is missing":  func(f map[string]string) { delete(f, policy.PathFactory) },
		"no feature catalogue":     func(f map[string]string) { delete(f, policy.PathFeatures) },
		"REVIEW.md is empty":       func(f map[string]string) { f[policy.PathReview] = "  \n" },
		"the manifest is not YAML": func(f map[string]string) { f[policy.PathFactory] = "factory_id: [" },
		"the manifest breaks its schema": func(f map[string]string) {
			f[policy.PathFactory] = strings.Replace(f[policy.PathFactory], "mode: docker", "mode: cluster", 1)
		},
		"the manifest is another factory's": func(f map[string]string) {
			f[policy.PathFactory] = strings.Replace(f[policy.PathFactory], "factory_id: acme-hello", "factory_id: someone-else", 1)
		},
		"the decision rules name no schema version": func(f map[string]string) {
			f[policy.PathDecisions] = "# DECISIONS.md\n\nImprove only on evidence.\n"
		},
		"the decision rules name two schema versions": func(f map[string]string) {
			f[policy.PathDecisions] = strings.Replace(f[policy.PathDecisions], "(schema version 1)", "(schema version 3)", 1)
		},
		"the catalogue has no table": func(f map[string]string) { f[policy.PathFeatures] = "# FEATURES.md\n\nEverything is allowed.\n" },
		"the catalogue lists a feature twice": func(f map[string]string) {
			// The catalogue already lists the Decision Layer; a second row
			// under the table's header names it again in other case.
			const separator = "|---|---|---|\n"
			f[policy.PathFeatures] = strings.Replace(f[policy.PathFeatures], separator, separator+"| decision layer | Brief §4.2 | built |\n", 1)
		},
		"the two catalogues disagree": func(f map[string]string) {
			f[policy.PathFeaturesYAML] = "features:\n  - name: Marketplace\n    scope: everything\n    status: built\n"
		},
		"the YAML catalogue has an unknown key": func(f map[string]string) {
			delete(f, policy.PathFeatures)
			f[policy.PathFeaturesYAML] = "features:\n  - name: Decision Layer\n    approved: true\n"
		},
		"a file is too large": func(f map[string]string) { f[policy.PathAgents] = strings.Repeat("a", policy.MaxFileSize+1) },
	}
	for name, edit := range cases {
		err := brokenAt(t, edit)
		if !core.HasCode(err, core.CodePolicyHold) {
			t.Errorf("%s: want %s, got %v", name, core.CodePolicyHold, err)
		}
	}
}

func TestTheCatalogueMayLiveInYAMLAloneOrAgreeInBothFiles(t *testing.T) {
	t.Parallel()
	const catalogue = "features:\n  - name: Decision Layer\n    scope: Brief §4.2\n    status: designed, not yet built\n"

	yamlOnly := newWorld(t)
	yamlOnly.repo.commit(revB, func(f map[string]string) {
		delete(f, policy.PathFeatures)
		f[policy.PathFeaturesYAML] = catalogue
	})
	if _, err := yamlOnly.baselines.Approve(context.Background(), yamlOnly.owner(), revB, ""); err != nil {
		t.Fatalf("a catalogue kept in features.yaml was refused: %v", err)
	}
	if s := yamlOnly.inForce(t); len(s.Catalog.Features) != 1 || !s.Catalog.Has("Decision Layer") {
		t.Fatalf("catalogue = %+v", s.Catalog.Features)
	}

	both := newWorld(t)
	both.repo.commit(revB, func(f map[string]string) {
		f[policy.PathFeatures] = "| Feature | Approved scope | Status |\n|---|---|---|\n| Decision Layer | Brief §4.2 | designed, not yet built |\n"
		f[policy.PathFeaturesYAML] = catalogue
	})
	if _, err := both.baselines.Approve(context.Background(), both.owner(), revB, ""); err != nil {
		t.Fatalf("two catalogues that say the same were refused: %v", err)
	}
}

func TestAHoldSaysWhyWithoutPickingARule(t *testing.T) {
	t.Parallel()
	err := brokenAt(t, func(f map[string]string) {
		f[policy.PathFeaturesYAML] = "features:\n  - name: Marketplace\n"
	})
	var typed *core.Error
	if !core.HasCode(err, core.CodePolicyHold) || !errors.As(err, &typed) {
		t.Fatalf("want a typed hold, got %v", err)
	}
	if typed.Details["reason"] != "conflict" || !strings.Contains(typed.Message, policy.PathFeaturesYAML) {
		t.Fatalf("the hold does not name the conflict: %+v", typed)
	}
}

func TestPolicyIsOnHoldWhileTheRepositoryCannotBeRead(t *testing.T) {
	t.Parallel()
	w := approved(t)
	w.repo.down = true
	_, err := w.loader.Current(context.Background())
	wantCode(t, err, core.CodePolicyHold, "policy while the repository is unreachable")
	w.repo.down = false
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("policy after recovery = %s", got.Revision)
	}
}

func TestContentThatDiffersFromWhatWasApprovedIsAHold(t *testing.T) {
	t.Parallel()
	w := approved(t)
	// The repository now returns other content for the approved commit, as
	// a wrong or compromised source would.
	w.repo.commit(revA, loosenedDecisions)
	_, err := w.loader.Current(context.Background())
	wantCode(t, err, core.CodePolicyHold, "content that no longer matches the approved digest")
}
