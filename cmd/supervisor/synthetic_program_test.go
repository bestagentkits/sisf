package main

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// The suite is wired only from an absolute program; without one a
// low-traffic release keeps holding, as before.
func TestTheSyntheticProgramMustBeAnAbsolutePath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		arg  string
		want bool
		bad  bool
	}{{"", false, false}, {"/opt/sisf/bin/hello-app-synthetic", true, false}, {"bin/hello-app-synthetic", false, true}} {
		var l lifecycleFlags
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		l.register(fs)
		if err := fs.Parse([]string{"--synthetic-program=" + tc.arg}); err != nil {
			t.Fatal(err)
		}
		suite, err := l.syntheticSuite(previewBackend{}, slog.Default())
		if (err != nil) != tc.bad || (suite != nil) != tc.want {
			t.Errorf("%q: suite %v, error %v", tc.arg, suite, err)
		}
	}
}
