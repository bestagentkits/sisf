//go:build unix

package clitest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Clock is a clock that moves only when a test moves it.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []timer
}

type timer struct {
	due time.Time
	ch  chan time.Time
}

// NewClock returns a clock standing at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now returns the instant the clock stands at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After delivers the instant once the clock has been advanced by d.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, timer{due: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward and fires every timer that came due.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	waiting := c.timers[:0]
	for _, t := range c.timers {
		if t.due.After(c.now) {
			waiting = append(waiting, t)
			continue
		}
		t.ch <- c.now
	}
	c.timers = waiting
}

// Subject is a built-in CLI backend under test.
type Subject interface {
	agent.Backend
	agent.Resumer
	agent.UsageReporter
	agent.RunInfoProvider
	agent.Checkpointer
	agent.HumanInputReceiver
	agent.SessionLinker
	agent.WebhookHandler
	Wait(ctx context.Context, run agent.RunRef) (agent.Status, error)
}

// Setup is what a backend under test is built with.
type Setup struct {
	Env         []string
	Clock       core.Clock
	ArtifactDir string
}

// Harness describes a backend to the conformance checks. The step lists are
// what the real tool would print, so they must first read the prompt the way
// the tool does.
type Harness struct {
	// Tool is the program name the backend runs.
	Tool string
	New  func(Setup) (Subject, error)
	// Route is a model route the backend accepts.
	Route core.ModelRoute
	// ForeignProvider is a provider the backend must refuse; empty when it
	// serves any provider.
	ForeignProvider string
	// Success is a run that finishes well; Failure one the tool reports as failed.
	Success, Failure []Step
	// Leaky returns a failing run whose output and error stream both carry token.
	Leaky func(token string) []Step
}

const (
	sha         = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	stubVersion = "9.9.9 (stub)"
)

var conformanceIssue = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}

func (h Harness) task(runID core.RunID, sow string) agent.Task {
	return agent.Task{
		RunID: runID, IssueID: conformanceIssue, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: sow, DoD: "The endpoint returns the build revision."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: h.Route},
		Limits:         agent.Limits{Timeout: 10 * time.Minute},
		IdempotencyKey: string(runID) + "/start",
	}
}

type subject struct {
	Subject
	clock *Clock
	ws    agent.WorkspaceRef
	art   string
}

func (h Harness) build(t *testing.T, script Script, extraEnv ...string) *subject {
	t.Helper()
	script.Version = stubVersion
	bin := Install(t, h.Tool, script)
	clock := NewClock(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC))
	art := t.TempDir()
	b, err := h.New(Setup{Env: Env(bin, extraEnv...), Clock: clock, ArtifactDir: art})
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	return &subject{Subject: b, clock: clock, ws: agent.WorkspaceRef{ID: "ws", Path: t.TempDir()}, art: art}
}

func (s *subject) start(t *testing.T, task agent.Task) agent.RunRef {
	t.Helper()
	ref, err := s.Start(context.Background(), task, s.ws, []string{"repo:read"})
	if err != nil {
		t.Fatalf("start: %v (cause: %v)", err, errors.Unwrap(err))
	}
	return ref
}

func (s *subject) wait(t *testing.T, ref agent.RunRef) agent.Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := s.Wait(ctx, ref)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	return status
}

func (s *subject) events(t *testing.T, ref agent.RunRef) []agent.Event {
	t.Helper()
	page, err := s.Events(context.Background(), ref, "")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return page.Events
}

// Conformance runs the checks every built-in CLI backend must pass: honest
// capabilities, the tool version in the run record, the time limit and the
// cancel killing the process group, credentials staying out of everything
// recorded, and resume from a handoff alone.
func Conformance(t *testing.T, h Harness) {
	t.Helper()
	t.Run("capabilities are declared and missing abilities answer NOT_SUPPORTED", func(t *testing.T) {
		t.Parallel()
		s := h.build(t, Script{})
		caps, err := s.Capabilities(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []core.Capability{core.CapAgentCancel, core.CapAgentResume, core.CapAgentUsage} {
			if !caps.Supports(c) {
				t.Errorf("%s should be supported", c)
			}
		}
		for _, c := range []core.Capability{core.CapAgentReplay, core.CapAgentCheckpoint, core.CapAgentHumanInput, core.CapAgentSessionLink, core.CapAgentWebhooks, core.CapAgentHardBudget} {
			if caps.Level(c) != core.SupportNotSupported {
				t.Errorf("%s must be NOT_SUPPORTED, got %s", c, caps.Level(c))
			}
		}
		ctx := context.Background()
		if _, err := s.Checkpoint(ctx, agent.RunRef{}); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("checkpoint: %v", err)
		}
		if err := s.HumanInput(ctx, agent.RunRef{}, "x"); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("human input: %v", err)
		}
		if _, err := s.SessionLink(ctx, agent.RunRef{}); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("session link: %v", err)
		}
		if _, _, err := s.HandleWebhook(ctx, nil, nil); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("webhook: %v", err)
		}
	})

	t.Run("a good run succeeds, records the tool version and leaves a transcript", func(t *testing.T) {
		t.Parallel()
		s := h.build(t, Script{Steps: h.Success})
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		if status := s.wait(t, ref); status.Phase != agent.PhaseSucceeded {
			t.Fatalf("phase %s (%s), want succeeded", status.Phase, status.Detail)
		}
		info, err := s.RunInfo(context.Background(), ref)
		if err != nil || info.ToolVersion != stubVersion || info.Tool != h.Tool || info.ModelRoute != h.Route {
			t.Errorf("run info %+v, %v: want tool %s at %s on %s", info, err, h.Tool, stubVersion, h.Route)
		}
		if first := s.events(t, ref)[0]; !strings.Contains(string(first.Payload), stubVersion) {
			t.Errorf("first event %s does not carry the tool version", first.Payload)
		}
		usage, err := s.Usage(context.Background(), ref)
		if err != nil || len(usage) != 1 || usage[0].Verified || usage[0].InputTokens+usage[0].OutputTokens == 0 {
			t.Errorf("usage %+v, %v: want the tool's token counts, unverified", usage, err)
		}
		artifacts, err := s.CollectArtifacts(context.Background(), ref)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		kinds := map[agent.ArtifactKind]bool{}
		for _, a := range artifacts {
			kinds[a.Kind] = true
		}
		if !kinds[agent.ArtifactTranscript] || !kinds[agent.ArtifactLog] {
			t.Errorf("artifacts %v lack the transcript or the log", kinds)
		}
	})

	t.Run("a run the tool reports as failed is failed and leaves nothing to collect", func(t *testing.T) {
		t.Parallel()
		s := h.build(t, Script{Steps: h.Failure})
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		if status := s.wait(t, ref); status.Phase != agent.PhaseFailed || status.Detail == "" {
			t.Errorf("status %+v, want failed with a reason", status)
		}
		if _, err := s.CollectArtifacts(context.Background(), ref); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("want CONFLICT, got %v", err)
		}
	})

	t.Run("a run past its time limit is killed with its process group", func(t *testing.T) {
		t.Parallel()
		pid := filepath.Join(t.TempDir(), "child.pid")
		s := h.build(t, Script{Steps: []Step{{SpawnChild: pid}, {Hang: true}}})
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		WaitForFile(t, pid)
		s.clock.Advance(10*time.Minute - time.Second)
		if status, _ := s.Status(context.Background(), ref); status.Phase != agent.PhaseRunning {
			t.Fatalf("phase %s just before the limit, want running", status.Phase)
		}
		s.clock.Advance(time.Second)
		if status := s.wait(t, ref); status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "time limit") {
			t.Errorf("status %+v, want failed on the time limit", status)
		}
		AssertProcessGone(t, pid)
	})

	t.Run("cancel kills the process group", func(t *testing.T) {
		t.Parallel()
		pid := filepath.Join(t.TempDir(), "child.pid")
		s := h.build(t, Script{Steps: []Step{{SpawnChild: pid}, {Hang: true}}})
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		WaitForFile(t, pid)
		if err := s.Cancel(context.Background(), ref); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if status, _ := s.Status(context.Background(), ref); status.Phase != agent.PhaseCancelled {
			t.Errorf("phase %s, want cancelled", status.Phase)
		}
		AssertProcessGone(t, pid)
	})

	t.Run("a token in the environment or the tool output reaches no log, event, artifact or error", func(t *testing.T) {
		t.Parallel()
		const (
			envToken    = "opaque-provider-credential-7731"
			outputToken = "sk-ant-api03-OUTPUTTOKENVALUE0099"
		)
		s := h.build(t, Script{Steps: append([]Step{{EchoEnv: "PROVIDER_ACCESS_TOKEN"}}, h.Leaky(envToken+" "+outputToken)...)},
			"PROVIDER_ACCESS_TOKEN="+envToken)
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		status := s.wait(t, ref)
		seen := []string{status.Detail}
		for _, e := range s.events(t, ref) {
			seen = append(seen, string(e.Payload))
		}
		info, _ := s.RunInfo(context.Background(), ref)
		seen = append(seen, fmt.Sprintf("%+v", info))
		// A failed run collects no artifacts; the log is read from a run that
		// succeeded with the same stream shape in the next check.
		for _, text := range seen {
			for _, secret := range []string{envToken, outputToken} {
				if strings.Contains(text, secret) {
					t.Errorf("secret %q leaked into %q", secret, text)
				}
			}
		}

		// The error of a start that cannot happen carries no environment either.
		bad := h.build(t, Script{}, "PROVIDER_ACCESS_TOKEN="+envToken)
		task := h.task("run-0002", "Add the version endpoint.")
		task.Policy.ModelRoute.Model = "--" + envToken
		if _, err := bad.Start(context.Background(), task, bad.ws, nil); err == nil || strings.Contains(err.Error(), envToken) {
			t.Errorf("a refused start must fail without echoing the environment, got %v", err)
		}
		missing, err := h.New(Setup{Env: []string{"PATH=" + t.TempDir(), "PROVIDER_ACCESS_TOKEN=" + envToken}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := missing.Start(context.Background(), h.task("run-0003", "x"), agent.WorkspaceRef{Path: t.TempDir()}, nil); err == nil || strings.Contains(err.Error(), envToken) {
			t.Errorf("a missing program must fail without echoing the environment, got %v", err)
		}
	})

	t.Run("tokens stay out of the artifacts of a run that succeeded", func(t *testing.T) {
		t.Parallel()
		const token = "sk-ant-api03-ARTIFACTTOKEN000042"
		steps := append([]Step{}, h.Success...)
		steps = append([]Step{{Stderr: "debug " + token}}, steps...)
		s := h.build(t, Script{Steps: steps}, "PROVIDER_ACCESS_TOKEN="+token)
		ref := s.start(t, h.task("run-0001", "Add the version endpoint."))
		if status := s.wait(t, ref); status.Phase != agent.PhaseSucceeded {
			t.Fatalf("status %+v", status)
		}
		artifacts, err := s.CollectArtifacts(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range artifacts {
			data, err := os.ReadFile(strings.TrimPrefix(a.URI, "file://"))
			if err != nil || strings.Contains(string(data), token) {
				t.Errorf("artifact %s: read error %v or token present", a.Name, err)
			}
		}
	})

	t.Run("a route of another provider or a model that looks like an option is refused before anything starts", func(t *testing.T) {
		t.Parallel()
		rec := t.TempDir()
		s := h.build(t, Script{Record: rec, Steps: h.Success})
		if h.ForeignProvider != "" {
			task := h.task("run-0001", "x")
			task.Policy.ModelRoute.Provider = h.ForeignProvider
			if _, err := s.Start(context.Background(), task, s.ws, nil); !core.HasCode(err, core.CodeNotSupported) {
				t.Errorf("foreign provider: want NOT_SUPPORTED, got %v", err)
			}
		}
		for name, mutate := range map[string]func(*agent.Task){
			"option-like model": func(k *agent.Task) { k.Policy.ModelRoute.Model = "--dangerously-skip-permissions" },
			"unknown effort":    func(k *agent.Task) { k.Policy.ModelRoute.Effort = "ludicrous" },
		} {
			task := h.task("run-0002", "x")
			mutate(&task)
			if _, err := s.Start(context.Background(), task, s.ws, nil); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(rec, "args.json")); err == nil {
			t.Error("the tool was started for a refused route")
		}
	})

	t.Run("resume starts a fresh process from the handoff alone", func(t *testing.T) {
		t.Parallel()
		rec := t.TempDir()
		steps := append([]Step{}, h.Success...)
		s := h.build(t, Script{Record: rec, Steps: steps})
		first := h.task("run-0001", "FIRSTSOWMARKER add the endpoint.")
		ref := s.start(t, first)
		if status := s.wait(t, ref); status.Phase != agent.PhaseSucceeded {
			t.Fatalf("first run: %+v", status)
		}
		if err := os.RemoveAll(rec); err != nil {
			t.Fatal(err)
		}
		next, err := s.Resume(context.Background(), ref, agent.Handoff{
			SoW: "RESUMEDSOWMARKER finish the endpoint.", DoD: "The endpoint returns the build revision.",
			Decisions: []string{"DECISIONMARKER use net/http"}, Evidence: []string{"EVIDENCEMARKER 3 tests pass"},
		})
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if status := s.wait(t, next); status.Phase != agent.PhaseSucceeded {
			t.Fatalf("resumed run: %+v", status)
		}
		seen := Recorded(t, rec)
		for _, want := range []string{"RESUMEDSOWMARKER", "DECISIONMARKER", "EVIDENCEMARKER"} {
			if !strings.Contains(seen.Stdin, want) {
				t.Errorf("the resumed agent was not given %s; it saw %q", want, seen.Stdin)
			}
		}
		if strings.Contains(seen.Stdin, "FIRSTSOWMARKER") {
			t.Error("the resumed agent was given the previous brief")
		}
		for _, arg := range seen.Args {
			switch arg {
			case "resume", "fork", "--resume", "-r", "--continue", "--session", "--session-id", "--fork-session", "--fork", "--last":
				t.Errorf("the resumed process was told to continue a session: %q", arg)
			}
		}
	})
}

// WaitForFile waits for a file the stand-in writes while it runs.
func WaitForFile(t testing.TB, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", filepath.Base(path))
}

// AssertProcessGone fails unless the process whose pid is in the file ends
// within a few seconds. A kill lands asynchronously, so it polls.
func AssertProcessGone(t testing.TB, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(raw), &pid); err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("process %d survived", pid)
}
