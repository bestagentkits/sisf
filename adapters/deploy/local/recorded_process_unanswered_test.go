package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A recorded process the system could not be asked about is not taken for
// gone: it counts as running, and its stop is not reported done.
func TestARecordedProcessThatCannotBeAskedAboutIsNotTakenForGone(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\necho no answer\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	// A start as ps prints it is read with ps on every system.
	rec := slotRecord{PID: os.Getpid(), Start: "Thu Nov 12 02:35:00 2026"}
	if same, err := sameProcess(rec); same || err == nil {
		t.Fatalf("sameProcess = %v, %v; want an error", same, err)
	}
	p := &recordedProcess{rec: rec, clock: core.SystemClock{}}
	if !p.Alive() {
		t.Fatal("a process that could not be asked about was reported gone")
	}
	if err := p.Stop(context.Background()); err == nil {
		t.Fatal("the stop of a process that could not be asked about was reported done")
	}
}
