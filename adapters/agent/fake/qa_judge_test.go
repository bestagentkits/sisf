package fake_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

const digest = core.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

func judgeRequest() qa.JudgeRequest {
	policy := core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
		ModelRoute: core.ModelRoute{Provider: "openai", Model: "mid"}}
	return qa.JudgeRequest{
		Candidate: qa.Candidate{IssueID: issue, RunID: "run-1", CommitSHA: sha, Digest: digest, BaselineSHA: sha,
			SuiteDigest: digest, Policy: policy, CodingAgentIdentity: "agent:coder",
			CodingModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}, Attempt: 1},
		Baseline:   qa.Baseline{CommitSHA: sha, PolicySHA: sha, SuiteDigest: digest, WorkspaceID: "qa-ws", ReadOnly: true},
		Identity:   "agent:qa",
		ModelRoute: policy.ModelRoute,
		Rubric:     qa.DefaultRubric(),
	}
}

func points(correctness int) map[qa.Category]int {
	return map[qa.Category]int{qa.CategoryCorrectness: correctness, qa.CategorySecurity: 25,
		qa.CategoryContract: 20, qa.CategoryExperience: 15, qa.CategoryObservability: 10}
}

func TestTheEvaluatorRoleJudgesByItsScript(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t, fake.Config{Roles: []agent.Role{agent.RoleCoder, agent.RoleQA}})
	if err := b.Script(agent.RoleQA, issue, fake.Score(points(10)), fake.Fail(0, "judge refused"), fake.Hang(), fake.Crash(0)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	card, err := b.Score(ctx, judgeRequest())
	if err != nil || card.Points[qa.CategoryCorrectness] != 10 || card.EvidenceRef == "" {
		t.Fatalf("scripted judgment = %+v, %v", card, err)
	}
	for _, want := range []core.ErrorCode{core.CodeUnavailable, core.CodeTimeout, core.CodeUnavailable} {
		if _, err := b.Score(ctx, judgeRequest()); !core.HasCode(err, want) {
			t.Errorf("judgment error = %v, want %s", err, want)
		}
	}
	card, err = b.Score(ctx, judgeRequest())
	if err != nil || card.Validate(qa.DefaultRubric()) != nil || card.Points[qa.CategoryCorrectness] != 30 {
		t.Fatalf("an unscripted judgment gives full marks, got %+v, %v", card, err)
	}
	if got := b.Judged(issue); len(got) != 5 || got[0].Identity != "agent:qa" {
		t.Fatalf("Judged recorded %d judgments", len(got))
	}
}

func TestTheEvaluatorRoleRefusesWhatItCannotJudge(t *testing.T) {
	t.Parallel()
	coderOnly, _ := newBackend(t, fake.Config{})
	if _, err := coderOnly.Score(context.Background(), judgeRequest()); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("a coder-only backend must not judge, got %v", err)
	}
	b, _ := newBackend(t, fake.Config{Roles: []agent.Role{agent.RoleQA}})
	cases := map[string]func(*qa.JudgeRequest){
		"anonymous evaluator": func(r *qa.JudgeRequest) { r.Identity = "" },
		"writable baseline":   func(r *qa.JudgeRequest) { r.Baseline.ReadOnly = false },
		"unbuilt candidate":   func(r *qa.JudgeRequest) { r.Candidate.Digest = "" },
		"no route":            func(r *qa.JudgeRequest) { r.ModelRoute = core.ModelRoute{} },
	}
	for name, mutate := range cases {
		req := judgeRequest()
		mutate(&req)
		if _, err := b.Score(context.Background(), req); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
	}
	if err := b.Script(agent.RoleQA, issue, fake.Score(points(31))); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("points above a category's weight must not be scripted, got %v", err)
	}
	if err := b.Script(agent.RoleQA, issue, fake.Patch(0, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Score(context.Background(), judgeRequest()); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a patch is not a judgment, got %v", err)
	}
}

func TestAnEvaluatorRunLeavesItsScoresAsATestReport(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t, fake.Config{Roles: []agent.Role{agent.RoleQA}})
	if err := b.Script(agent.RoleQA, issue, fake.Score(points(30))); err != nil {
		t.Fatal(err)
	}
	tk := task("run-1", "qa/run-1/1")
	tk.Role, tk.Step = agent.RoleQA, core.StepQA
	ref := start(t, b, tk)
	if got := phase(t, b, ref); got != agent.PhaseSucceeded {
		t.Fatalf("a scored run is %s, want succeeded", got)
	}
	artifacts, err := b.CollectArtifacts(context.Background(), ref)
	if err != nil || len(artifacts) != 1 || artifacts[0].Kind != agent.ArtifactTestReport || artifacts[0].Digest.Validate() != nil {
		t.Fatalf("artifacts = %+v, %v", artifacts, err)
	}
}
