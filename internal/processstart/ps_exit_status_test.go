package processstart

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// fakePS puts a ps that runs script first on PATH for the rest of the test.
func fakePS(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// Only a ps that exits with 1 and prints nothing says the process is gone,
// which is how ps answers for a pid no process has. A ps killed by a signal
// or exiting with another status said nothing about the process, so the
// start is unavailable rather than taken for gone.
func TestOnlyAPSThatExitsWithOneSaysTheProcessIsGone(t *testing.T) {
	for name, c := range map[string]struct {
		script string
		gone   bool
	}{
		"exits with 1":       {"exit 1", true},
		"killed by a signal": {"kill -9 $$", false},
		"exits with 2":       {"exit 2", false},
	} {
		t.Run(name, func(t *testing.T) {
			fakePS(t, c.script)
			start, err := psStartWithin(context.Background(), os.Getpid())
			switch {
			case c.gone && (start != "" || err != nil):
				t.Fatalf("psStartWithin = %q, %v; want the process gone", start, err)
			case !c.gone && (start != "" || !core.HasCode(err, core.CodeUnavailable)):
				t.Fatalf("psStartWithin = %q, %v; want UNAVAILABLE", start, err)
			}
		})
	}
}
