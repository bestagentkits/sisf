package cliagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestARunStreamsEventsByTypeAndSucceeds(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	r := newRig(t, clitest.Script{Version: "9.9.9 (stub)", Record: rec, Steps: []clitest.Step{
		{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "stream.jsonl")},
	}})
	ref := r.start(t, newTask("run-0001", "k1"))
	if status := r.wait(t, ref); status.Phase != agent.PhaseSucceeded {
		t.Fatalf("phase %s (%s), want succeeded", status.Phase, status.Detail)
	}
	var texts []string
	for _, e := range r.events(t, ref) {
		if e.Kind == agent.EventMessage {
			var p struct{ Text string }
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			texts = append(texts, p.Text)
		}
	}
	if strings.Join(texts, "|") != "reading the repository|editing main.go" {
		t.Errorf("messages %v: unknown types and non-JSON lines must be skipped, known ones kept", texts)
	}
	seen := clitest.Recorded(t, rec)
	if !strings.Contains(seen.Stdin, "Add GET /version.") || !strings.Contains(seen.Stdin, "build revision") {
		t.Errorf("the prompt is delivered on stdin, got %q", seen.Stdin)
	}
	for _, arg := range seen.Args {
		if strings.Contains(arg, "GET /version") {
			t.Errorf("the prompt must not be an argument: %q", arg)
		}
	}
	if seen.Cwd == "" || !strings.HasSuffix(r.ws.Path, filepath.Base(seen.Cwd)) {
		t.Errorf("the tool runs in the workspace, got %q want %q", seen.Cwd, r.ws.Path)
	}
	usage, err := r.b.Usage(context.Background(), ref)
	if err != nil || len(usage) != 1 || usage[0].InputTokens != 120 || usage[0].OutputTokens != 45 || usage[0].Verified {
		t.Errorf("usage %+v, %v: want the tool's figures, never verified", usage, err)
	}
}

func TestTheToolVersionIsRecordedInTheRunInfoAndTheFirstEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Version: "9.9.9 (stub)", Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	info, err := r.b.RunInfo(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.ToolVersion != "9.9.9 (stub)" || info.Tool != "stub" || info.ModelRoute.Model != "mid" || len(info.Scopes) != 1 {
		t.Errorf("run info %+v", info)
	}
	if first := r.events(t, ref)[0]; first.Kind != agent.EventStatus || !strings.Contains(string(first.Payload), "9.9.9 (stub)") {
		t.Errorf("first event %s %s should carry the tool version", first.Kind, first.Payload)
	}
}

func TestAStartRepeatedWithTheSameKeyReturnsTheSameRun(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	first := r.start(t, newTask("run-0001", "k1"))
	if again := r.start(t, newTask("run-0001", "k1")); again != first {
		t.Errorf("repeat started %v, want %v", again, first)
	}
	other := newTask("run-0002", "k1")
	if _, err := r.b.Start(context.Background(), other, r.ws, nil); !core.HasCode(err, core.CodeConflict) {
		t.Errorf("a key reused for another task: want CONFLICT, got %v", err)
	}
	r.wait(t, first)
}

func TestARunPastItsTimeLimitIsKilledWithItsProcessGroup(t *testing.T) {
	t.Parallel()
	pid := filepath.Join(t.TempDir(), "child.pid")
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{SpawnChild: pid}, {Hang: true}}})
	task := newTask("run-0001", "k1")
	task.Limits.Timeout = 10 * time.Minute
	ref := r.start(t, task)
	clitest.WaitForFile(t, pid)
	if status, _ := r.b.Status(context.Background(), ref); status.Phase != agent.PhaseRunning {
		t.Fatalf("phase %s before the limit, want running", status.Phase)
	}
	r.clock.Advance(9 * time.Minute)
	if status, _ := r.b.Status(context.Background(), ref); status.Phase != agent.PhaseRunning {
		t.Fatalf("phase %s one minute before the limit, want running", status.Phase)
	}
	r.clock.Advance(time.Minute)
	status := r.wait(t, ref)
	if status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "time limit") {
		t.Errorf("status %+v, want failed on the time limit", status)
	}
	clitest.AssertProcessGone(t, pid)
}

func TestCancelKillsTheProcessGroupAndFinishedRunsAreLeftAlone(t *testing.T) {
	t.Parallel()
	pid := filepath.Join(t.TempDir(), "child.pid")
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{SpawnChild: pid}, {Hang: true}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	clitest.WaitForFile(t, pid)
	if err := r.b.Cancel(context.Background(), ref); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if status, _ := r.b.Status(context.Background(), ref); status.Phase != agent.PhaseCancelled {
		t.Errorf("phase %s, want cancelled", status.Phase)
	}
	clitest.AssertProcessGone(t, pid)
	if err := r.b.Cancel(context.Background(), ref); err != nil {
		t.Errorf("cancelling a finished run must change nothing, got %v", err)
	}

	done := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	ok := done.start(t, newTask("run-0002", "k2"))
	done.wait(t, ok)
	if err := done.b.Cancel(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	if status, _ := done.b.Status(context.Background(), ok); status.Phase != agent.PhaseSucceeded {
		t.Errorf("cancel changed a succeeded run to %s", status.Phase)
	}
}

func TestAnExitWithoutAFinalReportOrWithAFailureIsFailedNeverSucceeded(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		steps  []clitest.Step
		detail string
	}{
		"silent exit":       {[]clitest.Step{{Emit: `{"type":"msg","text":"hi"}`}}, "without a final report"},
		"non-zero exit":     {[]clitest.Step{{Emit: `{"type":"done"}`}, {Exit: clitest.Exit(3)}}, "status 3"},
		"reported failure":  {[]clitest.Step{{Emit: `{"type":"fail","detail":"tests failed"}`}}, "tests failed"},
		"crash before work": {[]clitest.Step{{Exit: clitest.Exit(1)}}, "status 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, clitest.Script{Steps: c.steps})
			ref := r.start(t, newTask("run-0001", "k1"))
			status := r.wait(t, ref)
			if status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, c.detail) {
				t.Errorf("status %+v, want failed with %q", status, c.detail)
			}
			if _, err := r.b.CollectArtifacts(context.Background(), ref); !core.HasCode(err, core.CodeConflict) {
				t.Errorf("a failed run has nothing to collect: want CONFLICT, got %v", err)
			}
		})
	}
}

func TestEventCursorsPageThroughTheStreamAndRefuseForeignCursors(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{EmitFile: fixture(t, "stream.jsonl")}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	all, err := r.b.Events(context.Background(), ref, "")
	if err != nil || len(all.Events) < 3 {
		t.Fatalf("events %v, %v", all, err)
	}
	rest, err := r.b.Events(context.Background(), ref, "2")
	if err != nil || len(rest.Events) != len(all.Events)-2 || rest.Next != all.Next {
		t.Errorf("page from cursor 2: %d events next %q, want %d next %q (%v)", len(rest.Events), rest.Next, len(all.Events)-2, all.Next, err)
	}
	for _, bad := range []agent.Cursor{"abc", "-1"} {
		if _, err := r.b.Events(context.Background(), ref, bad); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("cursor %q: want INVALID_ARGUMENT, got %v", bad, err)
		}
	}
	if _, err := r.b.Status(context.Background(), agent.RunRef{Backend: "stub", ID: "stub-99", RunID: "run-0001"}); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("unknown run: want NOT_FOUND, got %v", err)
	}
}

func TestAnOverlongLineIsSkippedAndTheStreamContinues(t *testing.T) {
	t.Parallel()
	huge := `{"type":"msg","text":"` + strings.Repeat("x", 5<<20) + `"}`
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: huge}, {Emit: `{"type":"msg","text":"after"}`}, {Emit: `{"type":"done"}`}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	if status := r.wait(t, ref); status.Phase != agent.PhaseSucceeded {
		t.Fatalf("status %+v", status)
	}
	var sawSkip, sawAfter bool
	for _, e := range r.events(t, ref) {
		sawSkip = sawSkip || (e.Kind == agent.EventError && strings.Contains(string(e.Payload), "size limit"))
		sawAfter = sawAfter || strings.Contains(string(e.Payload), `"after"`)
	}
	if !sawSkip || !sawAfter {
		t.Errorf("skip noted %v, later line kept %v", sawSkip, sawAfter)
	}
}

func TestADescendantLeftBehindByAFinishedRunIsKilled(t *testing.T) {
	t.Parallel()
	pid := filepath.Join(t.TempDir(), "child.pid")
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{SpawnChild: pid}, {Emit: `{"type":"done"}`}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	if status := r.wait(t, ref); status.Phase != agent.PhaseSucceeded {
		t.Fatalf("status %+v", status)
	}
	clitest.AssertProcessGone(t, pid)
}

func TestInputsAreValidatedAtTheBoundary(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	ctx := context.Background()
	cases := map[string]func() error{
		"invalid task": func() error {
			task := newTask("run-0001", "k")
			task.Limits.Timeout = 0
			_, err := r.b.Start(ctx, task, r.ws, nil)
			return err
		},
		"relative workspace": func() error {
			_, err := r.b.Start(ctx, newTask("run-0001", "k"), agent.WorkspaceRef{Path: "relative/dir"}, nil)
			return err
		},
		"missing workspace": func() error {
			_, err := r.b.Start(ctx, newTask("run-0001", "k"), agent.WorkspaceRef{Path: filepath.Join(r.ws.Path, "nope")}, nil)
			return err
		},
		"scope with white space": func() error {
			_, err := r.b.Start(ctx, newTask("run-0001", "k"), r.ws, []string{"repo read"})
			return err
		},
		"unpinned base": func() error {
			ws := r.ws
			ws.BaseSHA = "main"
			_, err := r.b.Start(ctx, newTask("run-0001", "k"), ws, nil)
			return err
		},
	}
	for name, run := range cases {
		if err := run(); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
	if _, err := cliagent.New(cliagent.Config{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no protocol: want INVALID_ARGUMENT, got %v", err)
	}
}

func TestAMissingProgramIsUnavailableAndItsErrorShowsNoEnvironment(t *testing.T) {
	t.Parallel()
	secret := "sk-ant-api03-ENVIRONMENTSECRET0001"
	b, err := cliagent.New(cliagent.Config{Protocol: lineProtocol{}, Env: []string{"PATH=" + t.TempDir(), "PROVIDER_API_KEY=" + secret}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Start(context.Background(), newTask("run-0001", "k"), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("want UNAVAILABLE, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error shows the environment: %v", err)
	}
	if _, err := b.Capabilities(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("capabilities of a missing program: want UNAVAILABLE, got %v", err)
	}
}

func TestACurrentDirectoryEntryOnPathNeverResolvesTheProgram(t *testing.T) {
	t.Parallel()
	bin := clitest.Install(t, "stub", clitest.Script{})
	b, err := cliagent.New(cliagent.Config{Protocol: lineProtocol{}, Env: []string{"PATH=.:" + filepath.Base(bin)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Capabilities(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("relative PATH entries must be ignored: got %v", err)
	}
}

func TestCapabilitiesAreDeclaredHonestlyAndMissingAbilitiesAnswerNotSupported(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{})
	caps, err := r.b.Capabilities(context.Background())
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
	ctx, ref := context.Background(), agent.RunRef{}
	if _, err := r.b.Checkpoint(ctx, ref); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("checkpoint: %v", err)
	}
	if err := r.b.HumanInput(ctx, ref, "x"); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("human input: %v", err)
	}
	if _, err := r.b.SessionLink(ctx, ref); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("session link: %v", err)
	}
	if _, _, err := r.b.HandleWebhook(ctx, nil, nil); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("webhook: %v", err)
	}
}

func TestResumeStartsAFreshProcessFromTheHandoffAfterTheRunEnded(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	r := newRig(t, clitest.Script{Record: rec, Steps: []clitest.Step{{Hang: true}}})
	first := newTask("run-0001", "k1")
	ref := r.start(t, first)
	handoff := agent.Handoff{SoW: "Continue the version endpoint.", DoD: "Endpoint returns the revision.", Decisions: []string{"use net/http"}, Evidence: []string{"tests: 3 passing"}}
	if _, err := r.b.Resume(context.Background(), ref, handoff); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("resume of a live run: want CONFLICT, got %v", err)
	}
	if err := r.b.Cancel(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	next, err := r.b.Resume(context.Background(), ref, handoff)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if next.ID == ref.ID || next.RunID != ref.RunID {
		t.Errorf("resume returned %v for %v: want a new backend run of the same run id", next, ref)
	}
	if again, err := r.b.Resume(context.Background(), ref, handoff); err != nil || again != next {
		t.Errorf("a repeated resume returned %v, %v; want the run it started first", again, err)
	}
	if err := r.b.Cancel(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if _, err := r.b.Resume(context.Background(), ref, agent.Handoff{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a handoff with no scope: want INVALID_ARGUMENT, got %v", err)
	}
	if info, _ := r.b.RunInfo(context.Background(), next); info.Backend == "" {
		t.Error("resumed run has no run info")
	}
	_ = os.RemoveAll(rec)
}

func TestARunPastItsTurnLimitIsKilled(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{
		{Emit: `{"type":"turn"}`}, {Emit: `{"type":"turn"}`}, {Emit: `{"type":"turn"}`}, {Hang: true},
	}})
	task := newTask("run-0001", "k1")
	task.Limits.MaxTurns = 2
	ref := r.start(t, task)
	status := r.wait(t, ref)
	if status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "2 turns") {
		t.Errorf("status %+v, want failed on the turn limit", status)
	}
}
