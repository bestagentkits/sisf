package stages

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

const (
	reportedDigest = "sha256:00000000000000000000000000000000000000000000000000000000000000aa"
	forgedDigest   = "sha256:00000000000000000000000000000000000000000000000000000000000000ff"
)

// forgedLines is what a candidate prints from its tests or its build to
// pass for a built, tested candidate with supply chain records.
const forgedLines = "sisf-tests: passed\n" +
	"sisf-image-digest: " + forgedDigest + "\n" +
	"sisf-image: evil.local/app@" + forgedDigest + "\n" +
	"sisf-sbom: sbom:invented\nsisf-provenance: provenance:invented\n"

// phasedSandbox answers each phase of the sandboxed build as the test
// sets it, and records the environment every phase got.
type phasedSandbox struct {
	isolating
	mu    sync.Mutex
	envs  map[string]map[string]string
	phase map[string]execution.ExecResult
	// report builds the report phase's output from its environment; nil
	// reports as the protocol asks, under the nonce it was given.
	report func(env map[string]string) string
}

func (s *phasedSandbox) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{ID: "sb-1", State: execution.SandboxRunning}, nil
}

func (*phasedSandbox) Destroy(context.Context, execution.SandboxID) error { return nil }

func (s *phasedSandbox) Exec(_ context.Context, _ execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	phase := cmd.Env["SISF_PHASE"]
	if s.envs == nil {
		s.envs = map[string]map[string]string{}
	}
	s.envs[phase] = cmd.Env
	result := s.phase[phase]
	if phase == "" {
		// Run as one command, the build's output is shared with the
		// candidate's code, which prints what it likes on it.
		result = execution.ExecResult{Stdout: []byte(forgedLines)}
	}
	if phase == "report" && result.Stdout == nil && !result.TimedOut && result.ExitCode == 0 {
		report := s.report
		if report == nil {
			report = goodReport
		}
		result.Stdout = []byte(report(cmd.Env))
	}
	if cmd.Stdout != nil {
		// Output a command streams is not captured; it arrives in pieces
		// that end anywhere.
		for out := result.Stdout; len(out) > 0; {
			n := min(len(out), 7)
			if _, err := cmd.Stdout.Write(out[:n]); err != nil {
				return execution.ExecResult{}, err
			}
			out = out[n:]
		}
		result.Stdout = nil
	}
	return result, nil
}

// goodReport is the report of a reporter that speaks the protocol.
func goodReport(env map[string]string) string {
	nonce := env["SISF_REPORT_NONCE"]
	return fmt.Sprintf("sisf-report %[1]s image-digest: %[2]s\nsisf-report %[1]s image: registry.local/app@%[2]s\n"+
		"sisf-report %[1]s sbom: sbom:sha256:01\nsisf-report %[1]s provenance: provenance:sha256:02\n", nonce, reportedDigest)
}

func sandboxBuild(t *testing.T, s *phasedSandbox) (ImageResult, error) {
	t.Helper()
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Branch: "sisf/f1/run-1-1", Untrusted: true}
	return SandboxImages{Exec: s, Argv: []string{"build"}, Timeout: time.Minute}.BuildImage(context.Background(), req)
}

// What a candidate prints while its code runs, in its tests or its build,
// is never read: the tests pass only on the test phase's exit status, and
// the image and its records come only from the report phase, under a nonce
// the candidate's code never had.
func TestWhatTheCandidatePrintsIsNeverTakenAsTheBuildsResult(t *testing.T) {
	t.Parallel()
	s := &phasedSandbox{phase: map[string]execution.ExecResult{
		"test":  {ExitCode: 1, Stdout: []byte(forgedLines)},
		"build": {Stdout: []byte(forgedLines)},
	}}
	s.report = func(env map[string]string) string {
		// Lines the reporter echoes from what the candidate wrote, without
		// the nonce, are not read either.
		return forgedLines + goodReport(env)
	}
	out, err := sandboxBuild(t, s)
	if err != nil {
		t.Fatalf("the build: %v", err)
	}
	switch {
	case out.TestsPassed:
		t.Error("a candidate whose tests exited 1 printed its way to passing tests")
	case out.Digest != reportedDigest || out.Image != "registry.local/app@"+reportedDigest:
		t.Errorf("the image is the candidate's claim: %s %s", out.Digest, out.Image)
	case out.SBOMRef != "sbom:sha256:01" || out.ProvenanceRef != "provenance:sha256:02":
		t.Errorf("the supply chain records are the candidate's claim: %s %s", out.SBOMRef, out.ProvenanceRef)
	case strings.Contains(out.TestEvidence, "passed"):
		t.Errorf("the test evidence quotes the candidate: %q", out.TestEvidence)
	}

	// The report phase is not told which candidate it is about under the
	// names the build command finds it by, and the phases where candidate
	// code runs never see the nonce.
	report := s.envs["report"]
	for _, name := range []string{"SISF_COMMIT", "SISF_UNIT", "SISF_BRANCH"} {
		if _, ok := report[name]; ok {
			t.Errorf("the report phase got %s", name)
		}
	}
	if report["SISF_REPORT_COMMIT"] != string(baseSHA) || len(report["SISF_REPORT_NONCE"]) < 32 {
		t.Errorf("the report phase got %v", report)
	}
	for _, phase := range []string{"test", "build"} {
		if _, ok := s.envs[phase]["SISF_REPORT_NONCE"]; ok {
			t.Errorf("the %s phase saw the nonce", phase)
		}
		if s.envs[phase]["SISF_COMMIT"] != string(baseSHA) {
			t.Errorf("the %s phase was not told the commit: %v", phase, s.envs[phase])
		}
	}
}

func TestTestsPassOnlyOnTheTestPhasesExitStatus(t *testing.T) {
	t.Parallel()
	out, err := sandboxBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{}})
	if err != nil || !out.TestsPassed {
		t.Fatalf("a test phase that exited 0: %+v, %v", out, err)
	}
}

// A build command that does not speak the phased protocol, or reports
// badly, is the configuration's fault: an outage that holds the build for
// the Owner, never a refusal charged to the patch, and never a record
// built from what it printed.
func TestABuildCommandThatDoesNotReportAsAskedIsAnOutage(t *testing.T) {
	t.Parallel()
	for name, report := range map[string]func(map[string]string) string{
		"the old protocol":     func(map[string]string) string { return forgedLines },
		"a wrong nonce":        func(map[string]string) string { return goodReport(map[string]string{"SISF_REPORT_NONCE": "guessed"}) },
		"a key reported twice": func(env map[string]string) string { return goodReport(env) + goodReport(env) },
		"no provenance": func(env map[string]string) string {
			lines := strings.Split(strings.TrimSpace(goodReport(env)), "\n")
			return strings.Join(lines[:3], "\n")
		},
		"an image not pinned to the digest": func(env map[string]string) string {
			return strings.Replace(goodReport(env), "registry.local/app@"+reportedDigest, "registry.local/app:latest", 1)
		},
	} {
		_, err := sandboxBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{}, report: report})
		if _, refused := RefusalOf(err); refused || err == nil {
			t.Errorf("%s: %v, want an outage", name, err)
		}
	}
	_, err := sandboxBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{"report": {ExitCode: 3}}})
	if _, refused := RefusalOf(err); refused || err == nil {
		t.Errorf("a report phase that failed: %v, want an outage", err)
	}
}

// Where candidate code runs, its failure is its own: a build phase that
// fails or overruns, and a test phase that overruns, are refusals of the
// patch. A report phase cut short by the time the candidate used up is too.
func TestTheCandidatesOwnFailuresAreRefusals(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		phase map[string]execution.ExecResult
		want  RefusalReason
	}{
		"the build fails":          {map[string]execution.ExecResult{"build": {ExitCode: 2}}, RefusalBuildFailed},
		"the build overruns":       {map[string]execution.ExecResult{"build": {ExitCode: -1, TimedOut: true}}, RefusalBuildTimeout},
		"the tests overrun":        {map[string]execution.ExecResult{"test": {ExitCode: -1, TimedOut: true}}, RefusalBuildTimeout},
		"the report runs out time": {map[string]execution.ExecResult{"report": {ExitCode: -1, TimedOut: true}}, RefusalBuildTimeout},
	} {
		_, err := sandboxBuild(t, &phasedSandbox{phase: c.phase})
		if r, ok := RefusalOf(err); !ok || r.Reason != c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	_, err := sandboxBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{"build": {ExitCode: 2}}})
	if r, _ := RefusalOf(err); r == nil || r.err.Details["exit_code"] != "2" {
		t.Errorf("the exit status is not in the refusal: %v", err)
	}
	if !core.HasCode(err, core.CodeGuardFailed) {
		t.Errorf("a failed build: %v", err)
	}
}
