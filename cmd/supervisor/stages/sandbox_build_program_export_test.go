package stages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// exportedProgram is what a build exports in these tests: longer than one
// line of the report carries.
var exportedProgram = bytes.Repeat([]byte("\x7fELF the candidate's own build\n"), 40)

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// exportReport is a report that names digest and exports program in lines
// of base64, each decodable by itself.
func exportReport(env map[string]string, digest string, program []byte) string {
	nonce := env["SISF_REPORT_NONCE"]
	var b strings.Builder
	fmt.Fprintf(&b, "sisf-report %[1]s image-digest: %[2]s\nsisf-report %[1]s image: local.test/app@%[2]s\n", nonce, digest)
	for len(program) > 0 {
		n := min(len(program), 300)
		fmt.Fprintf(&b, "sisf-report %s program: %s\n", nonce, base64.StdEncoding.EncodeToString(program[:n]))
		program = program[n:]
	}
	return b.String()
}

func newProgramStore(t *testing.T) *programstore.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := programstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func exportingBuild(t *testing.T, s *phasedSandbox, store *programstore.Store) (ImageResult, error) {
	t.Helper()
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Branch: "sisf/f1/run-1-1", Untrusted: true}
	return SandboxImages{Exec: s, Argv: []string{"build"}, Timeout: time.Minute, Programs: store}.BuildImage(context.Background(), req)
}

// With a program store the report phase is told the supervisor's own
// operating system and architecture and exports the unit's program on its
// nonce-tagged lines. The image digest of the build is the sha256 of the
// bytes the supervisor received, and the store holds exactly those bytes
// under it.
func TestTheImageDigestIsTheDigestOfTheProgramTheSupervisorReceived(t *testing.T) {
	t.Parallel()
	store := newProgramStore(t)
	want := sha256Of(exportedProgram)
	s := &phasedSandbox{phase: map[string]execution.ExecResult{
		// What the candidate prints while its code runs is not a program.
		"build": {Stdout: []byte("sisf-report guessed program: " + base64.StdEncoding.EncodeToString([]byte("evil")) + "\n")},
	}}
	s.report = func(env map[string]string) string {
		// A line without the nonce exports nothing either.
		return "sisf-report guessed program: ZXZpbA==\n" + exportReport(env, want, exportedProgram)
	}
	out, err := exportingBuild(t, s, store)
	if err != nil {
		t.Fatalf("the build: %v", err)
	}
	if string(out.Digest) != want || out.Image != "local.test/app@"+want {
		t.Fatalf("the build is recorded as %s %s, want the digest of the received bytes %s", out.Digest, out.Image, want)
	}
	path, err := store.Path(out.Digest)
	if err != nil {
		t.Fatalf("the store does not hold the program: %v", err)
	}
	if kept, err := os.ReadFile(path); err != nil || !bytes.Equal(kept, exportedProgram) {
		t.Errorf("the store holds %d bytes (%v), not the %d the build exported", len(kept), err, len(exportedProgram))
	}
	report := s.envs["report"]
	if report["SISF_REPORT_TARGET_OS"] != runtime.GOOS || report["SISF_REPORT_TARGET_ARCH"] != runtime.GOARCH {
		t.Errorf("the report phase was not told the supervisor's target: %v", report)
	}
	for _, phase := range []string{"test", "build"} {
		if _, ok := s.envs[phase]["SISF_REPORT_TARGET_OS"]; ok {
			t.Errorf("the %s phase, where the candidate's code runs, was told the report's target", phase)
		}
	}
}

// A program that is not the one the report names, is missing, malformed or
// too large is the build command's fault: an outage that spends no attempt
// and is held for the Owner, and nothing of it is kept.
func TestAnExportThatIsNotTheReportedProgramIsAnOutageAndKeepsNothing(t *testing.T) {
	t.Parallel()
	good := sha256Of(exportedProgram)
	for name, tc := range map[string]struct {
		report func(env map[string]string) string
		limit  int64
		want   string
	}{
		"the report claims the digest of another program": {
			report: func(env map[string]string) string {
				return exportReport(env, sha256Of([]byte("the program QA is told about")), exportedProgram)
			},
			want: "the program it exported has digest " + good,
		},
		"a program cut short": {
			report: func(env map[string]string) string {
				return exportReport(env, good, exportedProgram[:len(exportedProgram)-1])
			},
			want: "the program it exported has digest",
		},
		"no program": {
			report: func(env map[string]string) string { return exportReport(env, good, nil) },
			want:   "exports no program",
		},
		"a program that is not base64": {
			report: func(env map[string]string) string {
				return exportReport(env, good, exportedProgram) + "sisf-report " + env["SISF_REPORT_NONCE"] + " program: not*base64\n"
			},
			want: "not base64",
		},
		"a program larger than the store receives": {
			report: func(env map[string]string) string { return exportReport(env, good, exportedProgram) },
			limit:  int64(len(exportedProgram) - 1), want: "larger than",
		},
		"a report line longer than is read": {
			report: func(env map[string]string) string {
				return exportReport(env, good, exportedProgram) + "sisf-report " + env["SISF_REPORT_NONCE"] + " program: " + strings.Repeat("QUFB", maxReportLine/4) + "\n"
			},
			want: "longer than",
		},
	} {
		store := newProgramStore(t)
		if tc.limit > 0 {
			store.MaxBytes = tc.limit
		}
		_, err := exportingBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{}, report: tc.report}, store)
		if _, refused := RefusalOf(err); refused || !core.HasCode(err, core.CodeUnavailable) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an outage that says %q", name, err, tc.want)
		}
		if removed, rerr := store.Retain(); rerr != nil || len(removed) != 0 {
			t.Errorf("%s: the store was left holding %v (%v)", name, removed, rerr)
		}
	}
}
