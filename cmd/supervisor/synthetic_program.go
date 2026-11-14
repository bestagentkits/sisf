package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const (
	// syntheticTimeout bounds one run of the application's synthetic suite.
	syntheticTimeout = 2 * time.Minute
	// syntheticOutputLimit bounds what of a run's output is kept for the log.
	syntheticOutputLimit = 4 << 10
)

// programSuite is the application's synthetic suite as a program the
// installer built from the application's own source. A release whose real
// traffic is too thin to judge is judged by it: the supervisor runs it
// against the address where the revision itself serves, with the revision
// the instance must report, and a run passes only when the program exits 0.
//
// The program runs with none of the supervisor's environment, so no token
// or key the supervisor holds in it reaches the program. It still runs as
// the supervisor's operating-system user and can read what that user can:
// what makes it trusted is that the installer builds it from the factory's
// own source at `sisf up`, never from a candidate. Its output is logged,
// bounded, and never becomes the release's evidence: the detail kept is
// fixed text.
type programSuite struct {
	program string
	backend deploy.Backend
	log     *slog.Logger
	// timeout bounds one run; zero is syntheticTimeout.
	timeout time.Duration
}

// Run runs the suite once against rev. A run that judged the revision is a
// result: the program exited, with 0 for a pass, or ran out of the time a
// run is given, which is a failed run. A run that judged nothing is an
// error: the revision has no address to reach, the program could not be
// started, or ctx ended before the program did.
func (s *programSuite) Run(ctx context.Context, rev deploy.Revision) (release.SyntheticResult, error) {
	preview, err := s.backend.Preview(ctx, rev)
	if err != nil {
		return release.SyntheticResult{}, err
	}
	if preview.URL == "" {
		return release.SyntheticResult{}, core.NewError(core.CodeReleaseHold, "the revision has no address the synthetic suite can reach")
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = syntheticTimeout
	}
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(run, s.program, "--url", preview.URL, "--revision", string(rev.Digest))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = 5 * time.Second
	out := &boundedBuffer{limit: syntheticOutputLimit}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		s.log.Error("synthetic suite could not be started", "unit", rev.Unit, "release", rev.Digest, "program", s.program, "error", err)
		return release.SyntheticResult{}, core.NewError(core.CodeUnavailable, "the synthetic suite could not be started").WithCause(err)
	}
	runErr := cmd.Wait()
	if runErr == nil {
		s.log.Info("synthetic suite passed", "unit", rev.Unit, "release", rev.Digest)
		return release.SyntheticResult{Passed: true, Detail: "the synthetic suite passed"}, nil
	}
	if ctx.Err() != nil {
		// The caller stopped waiting: the run was cut short, not judged.
		return release.SyntheticResult{}, fmt.Errorf("the synthetic suite was stopped before it finished: %w", ctx.Err())
	}
	var detail string
	var exit *exec.ExitError
	switch {
	case run.Err() != nil:
		detail = "the synthetic suite failed: it did not finish in time"
	case errors.As(runErr, &exit):
		detail = fmt.Sprintf("the synthetic suite failed with exit status %d", exit.ExitCode())
	default:
		// Wait failed for a reason of its own, such as output it could not
		// copy: the program's verdict is unknown.
		s.log.Error("synthetic suite could not be waited for", "unit", rev.Unit, "release", rev.Digest, "error", runErr)
		return release.SyntheticResult{}, core.NewError(core.CodeUnavailable, "the synthetic suite's run could not be read").WithCause(runErr)
	}
	s.log.Warn(detail, "unit", rev.Unit, "release", rev.Digest, "output", strings.TrimSpace(out.String()))
	return release.SyntheticResult{Detail: detail}, nil
}

// Ready reports whether the program can be run at all: it is still a
// regular file that may be executed. A low-traffic release holds before
// anything is deployed when it cannot.
func (s *programSuite) Ready(context.Context) error { return checkSyntheticProgram(s.program) }

// checkSyntheticProgram fails unless program names a regular file with an
// execute permission bit set.
func checkSyntheticProgram(program string) error {
	info, err := os.Stat(program)
	switch {
	case err != nil:
		return core.NewError(core.CodeUnavailable, fmt.Sprintf("the synthetic suite %s cannot be found", program)).WithCause(err)
	case !info.Mode().IsRegular():
		return core.NewError(core.CodeUnavailable, fmt.Sprintf("the synthetic suite %s is not a regular file", program))
	case info.Mode().Perm()&0o111 == 0:
		return core.NewError(core.CodeUnavailable, fmt.Sprintf("the synthetic suite %s is not executable", program))
	}
	return nil
}

// boundedBuffer keeps the first limit bytes written to it and drops the rest.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// syntheticSuite is the suite --synthetic-program names, nil without one:
// a release whose real traffic is too thin then holds, as before. A named
// program that is missing or cannot be executed fails the start.
func (l *lifecycleFlags) syntheticSuite(backend deploy.Backend, log *slog.Logger) (release.SyntheticSuite, error) {
	if l.syntheticProgram == "" {
		return nil, nil
	}
	if !filepath.IsAbs(l.syntheticProgram) {
		return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "--synthetic-program must be an absolute path"))
	}
	// A suite named but missing would leave every low-traffic release to
	// hold; the supervisor refuses to start instead, where it is seen.
	if err := checkSyntheticProgram(l.syntheticProgram); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "--synthetic-program: "+err.Error()).WithCause(err)
	}
	return &programSuite{program: l.syntheticProgram, backend: backend, log: log}, nil
}
