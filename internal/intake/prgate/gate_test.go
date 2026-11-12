package prgate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
)

type auditSpy struct{ events []core.AuditEvent }

func (a *auditSpy) Append(_ context.Context, e core.AuditEvent) (core.AuditEvent, error) {
	a.events = append(a.events, e)
	return e, nil
}
func (a *auditSpy) Verify(context.Context) error { return nil }

func sha(c string) core.CommitSHA { return core.CommitSHA(strings.Repeat(c, 40)) }

func pr(fork bool, assoc github.AuthorAssociation, login string, bot bool) github.PullRequest {
	return github.PullRequest{
		Repo: github.RepoRef{Owner: "acme", Name: "app"}, Number: 12, HeadSHA: sha("1"), IsFork: fork,
		Author: github.User{Login: login, Bot: bot}, AuthorAssociation: assoc,
	}
}

func newGate(t *testing.T, v prgate.Visibility) (*prgate.Gate, *auditSpy) {
	t.Helper()
	audit := &auditSpy{}
	g, err := prgate.New(prgate.Config{Visibility: v, AgentLogins: []string{"sisf-factory[bot]"}, Audit: audit,
		Clock: fixed{time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	return g, audit
}

type fixed struct{ t time.Time }

func (f fixed) Now() time.Time                       { return f.t }
func (f fixed) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

var (
	maintainer = core.Actor{ID: "u-maint", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}}
	reporter   = core.Actor{ID: "u-rep", Kind: core.ActorHuman, Roles: []core.Role{core.RoleReporter}}
	agentOwner = core.Actor{ID: "a-1", Kind: core.ActorAgent, Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}}
)

func TestPublicRepoForkRunsSandboxedWithoutSecretsUntilAccepted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, audit := newGate(t, prgate.VisibilityPublic)
	fork := pr(true, github.AssociationFirstTimeContributor, "stranger", false)

	d, err := g.Evaluate(ctx, fork)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stage != prgate.StageSandboxOnly || d.AllowSecrets || d.AllowRepoWrite || !d.NeedsAcceptance {
		t.Fatalf("an unaccepted fork must stay in the sandbox: %+v", d)
	}
	spec := execution.SandboxSpec{Trust: execution.TrustTrusted, SecretGrantIDs: []string{"grant-1"}}
	if err := d.ApplyTo(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Trust != execution.TrustUntrusted || len(spec.SecretGrantIDs) != 0 {
		t.Fatalf("the sandbox must hold no secrets and untrusted code: %+v", spec)
	}

	for name, actor := range map[string]core.Actor{"reporter": reporter, "agent with roles": agentOwner} {
		if err := g.Accept(ctx, actor, fork); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s must not accept, got %v", name, err)
		}
	}
	if d, _ := g.Evaluate(ctx, fork); d.Stage != prgate.StageSandboxOnly {
		t.Fatal("a refused acceptance must not admit the pull request")
	}

	if err := g.Accept(ctx, maintainer, fork); err != nil {
		t.Fatal(err)
	}
	d, _ = g.Evaluate(ctx, fork)
	if d.Stage != prgate.StagePipeline || !d.AllowSecrets || d.AllowRepoWrite || d.NeedsAcceptance {
		t.Fatalf("an accepted fork enters the pipeline but never gets repository write: %+v", d)
	}
	if n := len(audit.events); n != 4 || audit.events[0].Outcome != core.OutcomeDenied ||
		audit.events[2].Outcome != core.OutcomeAllowed || audit.events[3].Outcome != core.OutcomeExecuted {
		t.Fatalf("acceptances and refusals must be audited: %+v", audit.events)
	}

	pushed := fork
	pushed.HeadSHA = sha("2")
	if d, _ := g.Evaluate(ctx, pushed); d.Stage != prgate.StageSandboxOnly || d.AllowSecrets {
		t.Fatalf("a new push is not covered by the old acceptance: %+v", d)
	}
}

func TestPublicRepoCollaboratorBranchEntersThePipeline(t *testing.T) {
	t.Parallel()
	g, _ := newGate(t, prgate.VisibilityPublic)
	d, err := g.Evaluate(context.Background(), pr(false, github.AssociationCollaborator, "dev", false))
	if err != nil || d.Stage != prgate.StagePipeline || !d.AllowSecrets || !d.AllowRepoWrite {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	outsider, _ := g.Evaluate(context.Background(), pr(false, github.AssociationNone, "drive-by", false))
	if outsider.Stage != prgate.StageSandboxOnly || outsider.AllowSecrets {
		t.Fatalf("an outsider without a fork is still untrusted: %+v", outsider)
	}
}

func TestPrivateRepoAcceptsOnlyCollaboratorsAndFactoryAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, _ := newGate(t, prgate.VisibilityPrivate)
	for name, p := range map[string]github.PullRequest{
		"collaborator":  pr(false, github.AssociationMember, "dev", false),
		"factory agent": pr(false, github.AssociationNone, "sisf-factory[bot]", true),
	} {
		if d, err := g.Evaluate(ctx, p); err != nil || d.Stage != prgate.StagePipeline {
			t.Errorf("%s: %+v, %v", name, d, err)
		}
	}
	for name, p := range map[string]github.PullRequest{
		"outsider":               pr(false, github.AssociationContributor, "ex-member", false),
		"outsider fork":          pr(true, github.AssociationNone, "stranger", false),
		"human using agent name": pr(false, github.AssociationNone, "sisf-factory[bot]", false),
		"other bot":              pr(false, github.AssociationNone, "random[bot]", true),
	} {
		d, err := g.Evaluate(ctx, p)
		if err != nil || d.Stage != prgate.StageRejected || d.AllowSecrets {
			t.Errorf("%s must be rejected: %+v, %v", name, d, err)
		}
		if err := d.ApplyTo(&execution.SandboxSpec{}); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: a rejected pull request must not get a sandbox, got %v", name, err)
		}
		if err := g.Accept(ctx, maintainer, p); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: not even a Maintainer can admit an outsider to a private repo, got %v", name, err)
		}
	}
	collabFork := pr(true, github.AssociationCollaborator, "dev", false)
	if d, _ := g.Evaluate(ctx, collabFork); d.Stage != prgate.StageSandboxOnly {
		t.Errorf("a fork is sandboxed until accepted, also in a private repo: %+v", d)
	}
}

func TestGateRefusesBadConfigurationAndMalformedPullRequests(t *testing.T) {
	t.Parallel()
	if _, err := prgate.New(prgate.Config{Visibility: "internal"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unknown visibility must fail closed, got %v", err)
	}
	g, _ := newGate(t, prgate.VisibilityPublic)
	bad := pr(true, github.AssociationNone, "x", false)
	bad.HeadSHA = "main"
	if _, err := g.Evaluate(context.Background(), bad); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a pull request without a pinned head must be refused, got %v", err)
	}
	if err := g.Accept(context.Background(), maintainer, bad); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an acceptance must bind to a pinned head, got %v", err)
	}
}

type brokenStore struct{}

func (brokenStore) Put(context.Context, prgate.Acceptance) error { return errors.New("down") }
func (brokenStore) Get(context.Context, github.RepoRef, int) (prgate.Acceptance, bool, error) {
	return prgate.Acceptance{}, false, errors.New("down")
}

func TestAcceptanceStoreFailureKeepsThePullRequestSandboxed(t *testing.T) {
	t.Parallel()
	g, err := prgate.New(prgate.Config{Visibility: prgate.VisibilityPublic, Acceptances: brokenStore{}})
	if err != nil {
		t.Fatal(err)
	}
	fork := pr(true, github.AssociationNone, "stranger", false)
	if _, err := g.Evaluate(context.Background(), fork); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("an unreadable acceptance must be an error, got %v", err)
	}
	if err := g.Accept(context.Background(), maintainer, fork); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("an unrecorded acceptance must be an error, got %v", err)
	}
}

type failingAudit struct{}

func (failingAudit) Verify(context.Context) error { return nil }

func (failingAudit) Append(context.Context, core.AuditEvent) (core.AuditEvent, error) {
	return core.AuditEvent{}, errors.New("audit down")
}

func TestUnauditedAcceptanceDoesNotTakeEffect(t *testing.T) {
	t.Parallel()
	g, _ := prgate.New(prgate.Config{Visibility: prgate.VisibilityPublic, Audit: failingAudit{}})
	fork := pr(true, github.AssociationNone, "stranger", false)
	if err := g.Accept(context.Background(), maintainer, fork); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("want %s, got %v", core.CodeUnavailable, err)
	}
	if d, _ := g.Evaluate(context.Background(), fork); d.Stage != prgate.StageSandboxOnly || d.AllowSecrets {
		t.Fatalf("an acceptance that could not be audited must not count: %+v", d)
	}
}
