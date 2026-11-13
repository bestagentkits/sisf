package harness

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Release parameters of the harness: one-minute windows with enough
// traffic in each that a 5% canary is measured, not held as low traffic.
const (
	ReleaseWindow    = time.Minute
	TrafficPerWindow = 400
	minSamples       = 10
)

// releaseClock is the harness clock as the release pipeline sees it: a
// wait for an observation window lets the window's traffic through the
// edges halfway through it, then moves the clock past it. No test sleeps.
type releaseClock struct {
	*sqlstoretest.Clock
	traffic func()
}

func (c releaseClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d / 2)
	c.traffic()
	c.Advance(d - d/2)
	fired := make(chan time.Time, 1)
	fired <- c.Now()
	return fired
}

// probes counts, per unit, the requests sent through its edge and those
// that failed with a server error.
type probes struct {
	mu            sync.Mutex
	sent, errored map[string]int
}

// startLifecycle wires the agent zone's patch exporter and the
// supervisor's build, evaluation and release stages, as factoryd and the supervisor do.
func (f *Factory) startLifecycle(client *github.RESTClient) {
	f.t.Helper()
	m := f.Manifest
	repo := github.RepoRef{Owner: m.Repository.Owner, Name: m.Repository.Repo}
	f.Trust = newTrust(f.t, m.FactoryID, f.Clock)
	f.Deploy = newDeployment(f.t, f.Clock, f.Trust.Installed)
	f.Artifacts = lifecycle.NewMemoryArtifacts()
	f.probes = &probes{sent: map[string]int{}, errored: map[string]int{}}
	clock := releaseClock{Clock: f.Clock, traffic: f.traffic}

	images, err := stages.SelectImageBuilder(context.Background(), stages.ExecutionChoice{Profile: stages.ProfileSimulation, Agent: "fake"})
	must(f.t, err)
	var artifacts lifecycle.ArtifactSource = f.Artifacts
	if f.opts.CoderArtifacts != nil {
		artifacts = f.opts.CoderArtifacts
	}
	f.Exporter = &lifecycle.Exporter{Owner: "exporter@harness", State: f.AgentStore, Artifacts: artifacts, Clock: f.Clock}
	f.Freeze, err = killswitch.New(killswitch.Config{FactoryID: m.FactoryID, State: f.ControlStore, Clock: f.Clock,
		Runs: killswitch.RunCanceller(f.coder())})
	must(f.t, err)
	var judge qa.Judge = f.Agent
	if f.opts.QAJudge != nil {
		judge, err = f.opts.QAJudge(stages.CandidateMaterials{State: f.ControlStore, Tree: stages.Tree{GitHub: client, Repo: repo}})
		must(f.t, err)
	}
	merger, err := github.NewMerger(client)
	must(f.t, err)
	declared := map[string][]string{}
	for unit, patterns := range m.Release.UnitInputs {
		declared[string(unit)] = patterns
	}
	inputs, err := release.NewUnitInputs(declared)
	must(f.t, err)
	f.Stages, err = stages.New(stages.Config{
		FactoryID: m.FactoryID, Repo: repo, State: f.ControlStore, GitHub: client, Judge: judge, Merger: merger,
		QAIdentity: "agent:qa/" + string(m.FactoryID), QAWorkspace: "qa/" + string(m.FactoryID),
		Self:     github.Identity{ID: f.GitHub.Bot().ID, Login: f.GitHub.Bot().Login},
		QASigner: f.Trust.QA, ArtifactSigner: f.Trust.Artifact, Delegation: f.Trust.Delegation, Verifier: f.Trust.Verifier, ApprovalVerifier: f.Trust.Verifier,
		Redactor: redact.New(redact.Config{}), MaxAttempts: m.Limits.MaxAttempts, Images: images, Clock: clock,
		Budget: budget.NewGate(budget.Config{Clock: f.Clock}),
		Release: release.Config{
			Backend: f.Deploy, LKG: f.Trust.LKG, Freeze: f.Freeze,
			SelfPublishable: unitNames(m.Release.Units), UnitInputs: inputs,
			Monitor: release.MonitorConfig{Window: ReleaseWindow, MinSamples: minSamples},
		},
	})
	must(f.t, err)
}

// traffic sends one window's requests through every unit's edge.
func (f *Factory) traffic() {
	for unit := range f.Trust.Installed {
		failed := f.Deploy.Traffic(unit, TrafficPerWindow)
		f.probes.mu.Lock()
		f.probes.sent[unit] += TrafficPerWindow
		f.probes.errored[unit] += failed
		f.probes.mu.Unlock()
	}
}

// Probed returns how many requests went through the edge of unit while
// releases were watched, and how many of them failed with a server error.
func (f *Factory) Probed(unit string) (sent, serverErrors int) {
	f.probes.mu.Lock()
	defer f.probes.mu.Unlock()
	return f.probes.sent[unit], f.probes.errored[unit]
}

// ScriptPatch makes the next coding attempt on issue number leave diff,
// after work.
func (f *Factory) ScriptPatch(number int, work time.Duration, diff string) {
	f.t.Helper()
	f.Artifacts.Put([]byte(diff))
	must(f.t, f.Agent.Script(agent.RoleCoder, f.Issue(number), fake.Patch(work, diff)))
}

// ScriptQA makes the next evaluation of issue number score points.
func (f *Factory) ScriptQA(number int, points map[qa.Category]int) {
	f.t.Helper()
	must(f.t, f.Agent.Script(agent.RoleQA, f.Issue(number), fake.Score(points)))
}

// Quiet stops the users' traffic between releases: a release then finds
// no real traffic to judge the last known good release by.
func (f *Factory) Quiet() { f.quiet = true }

// Busy brings the users' traffic back after Quiet.
func (f *Factory) Busy() { f.quiet = false }

// Cycle runs rounds of the whole factory: the control zone's pass, a
// dispatcher tick, the agent zone's exporter and the supervisor's stages,
// with one window of users' traffic through every unit before the stages.
// It fails the test on any error any of them reports.
func (f *Factory) Cycle(rounds int) {
	f.t.Helper()
	ctx := context.Background()
	for range rounds {
		f.Pass()
		f.Tick()
		if res := f.Exporter.Pass(ctx); len(res.Errors) > 0 {
			f.t.Fatalf("harness: exporter: %v", res.Errors)
		}
		if !f.quiet {
			// Users keep using the units while the factory works.
			f.traffic()
		}
		if errs := f.Stages.Pass(ctx).Errors(); len(errs) > 0 {
			f.t.Fatalf("harness: supervisor stages: %v", errs)
		}
		f.Pass()
	}
}

// unitNames returns units as the release pipeline names them. The result is
// never nil, so a manifest naming no unit publishes nothing.
func unitNames(units []manifest.UnitName) []string {
	names := make([]string, 0, len(units))
	for _, u := range units {
		names = append(names, string(u))
	}
	return names
}
