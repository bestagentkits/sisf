package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
}

// Run runs the suite once against rev. A revision with no address to reach
// is an error; a program that fails, cannot start or runs out of time is a
// failed run.
func (s *programSuite) Run(ctx context.Context, rev deploy.Revision) (release.SyntheticResult, error) {
	preview, err := s.backend.Preview(ctx, rev)
	if err != nil {
		return release.SyntheticResult{}, err
	}
	if preview.URL == "" {
		return release.SyntheticResult{}, core.NewError(core.CodeReleaseHold, "the revision has no address the synthetic suite can reach")
	}
	ctx, cancel := context.WithTimeout(ctx, syntheticTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.program, "--url", preview.URL, "--revision", string(rev.Digest))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.WaitDelay = 5 * time.Second
	out := &boundedBuffer{limit: syntheticOutputLimit}
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	if runErr == nil {
		s.log.Info("synthetic suite passed", "unit", rev.Unit, "release", rev.Digest)
		return release.SyntheticResult{Passed: true, Detail: "the synthetic suite passed"}, nil
	}
	var detail string
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		detail = "the synthetic suite failed: it did not finish in time"
	case errors.As(runErr, &exit):
		detail = fmt.Sprintf("the synthetic suite failed with exit status %d", exit.ExitCode())
	default:
		detail = "the synthetic suite failed: it could not be started"
	}
	s.log.Warn(detail, "unit", rev.Unit, "release", rev.Digest, "output", strings.TrimSpace(out.String()))
	return release.SyntheticResult{Detail: detail}, nil
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
// a release whose real traffic is too thin then holds, as before.
func (l *lifecycleFlags) syntheticSuite(backend deploy.Backend, log *slog.Logger) (release.SyntheticSuite, error) {
	if l.syntheticProgram == "" {
		return nil, nil
	}
	if !filepath.IsAbs(l.syntheticProgram) {
		return nil, core.NewError(core.CodeInvalidArgument, "--synthetic-program must be an absolute path")
	}
	return &programSuite{program: l.syntheticProgram, backend: backend, log: log}, nil
}
