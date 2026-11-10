//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// commandRunner runs the external programs of the smoke run: gh, claude,
// codex, go and the built sisf. Every command line is recorded in the
// report; output is returned to the caller unredacted (it may need to parse
// a token) and is redacted before anything logs or reports it.
type commandRunner struct {
	t      *testing.T
	dir    string
	scrub  *secretScrubber
	report *smokeReport
}

// commandError carries a failed command's redacted output for evidence.
type commandError struct {
	line   string
	err    error
	output string
}

func (e *commandError) Error() string {
	return fmt.Sprintf("%s: %v\n%s", e.line, e.err, lastLines(e.output, 30))
}

// run executes name with args, bounded by limit and ctx, in env (nil means
// the caller's environment). It returns standard output.
func (r *commandRunner) run(ctx context.Context, limit time.Duration, env []string, name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.report.Commands = addUnique(r.report.Commands, r.scrub.clean(line)) // polls repeat a line
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = r.dir, &stdout, &stderr
	if env != nil {
		cmd.Env = env
	}
	// A killed command must not hold its pipes open past the deadline.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return stdout.String(), &commandError{
			line: r.scrub.clean(line), err: err,
			output: r.scrub.clean(stdout.String() + stderr.String()),
		}
	}
	return stdout.String(), nil
}

// logf writes a redacted line to the test log.
func (r *commandRunner) logf(format string, args ...any) {
	r.t.Helper()
	r.t.Log(r.scrub.clean(fmt.Sprintf(format, args...)))
}

// fatalf records the failure in the report, then fails the test at once.
func (r *commandRunner) fatalf(format string, args ...any) {
	r.t.Helper()
	msg := r.scrub.clean(fmt.Sprintf(format, args...))
	r.report.Deviations = append(r.report.Deviations, msg)
	r.t.Fatal(msg)
}

// errorf records the failure in the report and marks the test failed.
func (r *commandRunner) errorf(format string, args ...any) {
	r.t.Helper()
	msg := r.scrub.clean(fmt.Sprintf(format, args...))
	r.report.Deviations = append(r.report.Deviations, msg)
	r.t.Error(msg)
}

// buildBinary compiles one package of this module into out.
func (r *commandRunner) buildBinary(ctx context.Context, out, pkg string) {
	r.t.Helper()
	if _, err := r.run(ctx, 5*time.Minute, nil, "go", "build", "-o", out, pkg); err != nil {
		r.fatalf("build %s: %v", pkg, err)
	}
}

// sisfEnv is the environment of the sisf command line: the caller's,
// without any SISF_ setting leaking in, plus the given assignments.
func sisfEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SISF_") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// minimalEnv is what a flag probe of a built program gets: no credential
// of the caller's at all.
func minimalEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
