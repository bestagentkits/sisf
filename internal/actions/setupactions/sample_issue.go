// Package setupactions serves setup.sample_issue, the last step of the
// first-time setup wizard: it opens one small, real issue in the factory's
// repository, labelled ready, so the Owner watches it go through the whole
// lifecycle. Intake still checks it like any other issue.
package setupactions

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// ActionSampleIssue is the name of the action this package serves.
const ActionSampleIssue = "setup.sample_issue"

// The sample issue. It asks for a small, observable and reversible change.
const (
	SampleTitle = "Sample issue: add GET /version returning the build revision"
	SampleBody  = "This issue was opened by the setup wizard to run one change through the factory's whole lifecycle.\n\n" +
		"Add an HTTP endpoint `GET /version` that returns the build revision of the running application as plain text.\n\n" +
		"Done when: the endpoint answers 200 with the revision, a test covers it, and nothing else changes."
)

const (
	sampleInput  = `{"type":"object","additionalProperties":false}`
	sampleOutput = `{"type":"object","properties":{"issue":{"type":"string"},"labels":{"type":"array","items":{"type":"string"}}}}`
)

// IssueOpener opens an issue; the GitHub REST client is one.
type IssueOpener interface {
	CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error)
}

// Spec returns the spec of setup.sample_issue.
func Spec() core.ActionSpec {
	return core.ActionSpec{Name: ActionSampleIssue,
		Summary:     "Open one small sample issue, labelled ready, that runs through the factory's whole lifecycle; Owner only.",
		Sensitivity: core.SensitivityWrite, Scopes: []string{"setup:write"}, Roles: []core.Role{core.RoleOwner},
		Input: actions.Schema(sampleInput), Output: actions.Schema(sampleOutput)}
}

// Actions returns setup.sample_issue, opening issues in repo.
func Actions(repo github.RepoRef, opener IssueOpener) ([]core.Action, error) {
	if opener == nil || repo.Owner == "" || repo.Name == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the sample issue needs the factory's repository and a GitHub client")
	}
	return []core.Action{{Spec: Spec(), Handle: func(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
		if _, err := actions.DecodeInput[struct{}](req); err != nil {
			return core.ActionResult{}, err
		}
		labels := []string{github.LabelReady, github.LabelKindImprovement}
		if req.DryRun {
			after, _ := json.Marshal(map[string]any{"repo": repo.Owner + "/" + repo.Name, "title": SampleTitle, "labels": labels})
			return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{Resource: "issues", Op: core.ChangeCreate, After: after}},
				Warnings: []string{"Opens a real issue that the factory will work on, spending from the budget like any other."}}}, nil
		}
		issue, err := opener.CreateIssue(ctx, repo, github.NewIssue{Title: SampleTitle, Body: SampleBody, Labels: labels})
		if err != nil {
			return core.ActionResult{}, core.NewError(core.CodeUnavailable, "GitHub did not open the sample issue").WithCause(err)
		}
		return actions.JSONResult(map[string]any{"issue": issue.Ref.String(), "labels": labels})
	}}}, nil
}
