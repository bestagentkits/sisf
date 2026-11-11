package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// previewBackend says where a revision can be reached.
type previewBackend struct {
	deploy.Backend
	url string
	err error
}

func (b previewBackend) Preview(_ context.Context, rev deploy.Revision) (deploy.Preview, error) {
	return deploy.Preview{Revision: rev, URL: b.url}, b.err
}

// syntheticScript writes a program that records its arguments and its
// environment and exits with status.
func syntheticScript(t *testing.T, status string) (program, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	program = filepath.Join(dir, "suite")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nenv >> '" + argsFile + "'\necho 'check page counts visits: GET / answered 500'\nexit " + status + "\n"
	if err := os.WriteFile(program, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, argsFile
}

// The application's synthetic suite judges a low-traffic release: the
// supervisor runs it against the revision's own address with the revision
// it must report, with none of the supervisor's environment, and a run
// passes only when the program exits 0.
func TestTheSyntheticProgramJudgesTheRevisionWhereItsPreviewServes(t *testing.T) {
	t.Setenv("EDGE_ADMIN_TOKEN", "edge-admin-secret")
	ctx := context.Background()
	rev := deploy.Revision{Unit: "app", Digest: core.ReleaseDigest("sha256:" + strings.Repeat("a", 64)), ID: "green"}
	program, argsFile := syntheticScript(t, "0")
	var log bytes.Buffer
	suite := &programSuite{program: program, backend: previewBackend{url: "http://127.0.0.1:7462"}, log: slog.New(slog.NewTextHandler(&log, nil))}
	result, err := suite.Run(ctx, rev)
	if err != nil || !result.Passed {
		t.Fatalf("a suite that exits 0: %+v, %v", result, err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.HasPrefix(got, "--url\nhttp://127.0.0.1:7462\n--revision\n"+string(rev.Digest)+"\n") || strings.Contains(got, "edge-admin-secret") {
		t.Errorf("the suite was run with:\n%s", got)
	}

	failing, _ := syntheticScript(t, "1")
	suite.program = failing
	result, err = suite.Run(ctx, rev)
	if err != nil || result.Passed || !strings.Contains(result.Detail, "failed") {
		t.Fatalf("a suite that exits 1: %+v, %v", result, err)
	}
	if !strings.Contains(log.String(), "GET / answered 500") {
		t.Errorf("the failing suite's output is not logged:\n%s", log.String())
	}

	suite.backend = previewBackend{err: core.NewError(core.CodeReleaseHold, "the revision is not running")}
	if _, err := suite.Run(ctx, rev); err == nil {
		t.Fatal("a revision with no preview was judged")
	}
}

// The suite is wired only from an absolute program that exists and may be
// executed; without one a low-traffic release keeps holding, as before,
// and a named program that is missing or cannot be run fails the start
// with a message that names it.
func TestTheSyntheticProgramMustBeAnExecutableAtAnAbsolutePath(t *testing.T) {
	t.Parallel()
	program, _ := syntheticScript(t, "0")
	dir := t.TempDir()
	plain := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		arg     string
		want    bool
		failure string
	}{
		{"", false, ""},
		{program, true, ""},
		{"bin/hello-app-synthetic", false, "absolute path"},
		{filepath.Join(dir, "missing"), false, "cannot be found"},
		{plain, false, "is not executable"},
		{dir, false, "is not a regular file"},
	} {
		var l lifecycleFlags
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		l.register(fs)
		if err := fs.Parse([]string{"--synthetic-program=" + tc.arg}); err != nil {
			t.Fatal(err)
		}
		suite, err := l.syntheticSuite(previewBackend{}, slog.Default())
		if (suite != nil) != tc.want || (err != nil) != (tc.failure != "") ||
			(err != nil && (!strings.Contains(err.Error(), tc.failure) || !strings.Contains(err.Error(), "--synthetic-program"))) {
			t.Errorf("%q: suite %v, error %v", tc.arg, suite, err)
		}
	}
}

// A run that judged nothing is an error, never a failed run: a program
// that cannot be started, one removed after the start, and a run the
// caller stopped. Only a run that outlasts its own time is a failed run.
func TestASyntheticRunThatJudgedNothingIsAnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rev := deploy.Revision{Unit: "app", Digest: core.ReleaseDigest("sha256:" + strings.Repeat("b", 64)), ID: "green"}
	backend := previewBackend{url: "http://127.0.0.1:1"}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	missing := &programSuite{program: filepath.Join(t.TempDir(), "gone"), backend: backend, log: quiet}
	if err := missing.Ready(ctx); err == nil {
		t.Error("a missing program is ready")
	}
	if result, err := missing.Run(ctx, rev); err == nil || result.Passed || result.Detail != "" {
		t.Errorf("a program that cannot be started: %+v, %v", result, err)
	}

	sleeper := filepath.Join(t.TempDir(), "suite")
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	slow := &programSuite{program: sleeper, backend: backend, log: quiet}
	if err := slow.Ready(ctx); err != nil {
		t.Fatalf("an executable program is not ready: %v", err)
	}
	stopped, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if result, err := slow.Run(stopped, rev); err == nil || result.Passed || result.Detail != "" {
		t.Errorf("a run the caller stopped: %+v, %v", result, err)
	}

	slow.timeout = 200 * time.Millisecond
	result, err := slow.Run(ctx, rev)
	if err != nil || result.Passed || !strings.Contains(result.Detail, "did not finish in time") {
		t.Errorf("a run that outlasted its own time: %+v, %v", result, err)
	}
}
