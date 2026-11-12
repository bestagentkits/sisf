package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The local deploy backend keeps its slot records in a directory of the
// trust volume. A supervisor started again over the same volume, after the
// directory was made and holds a file, builds the backend as the first one
// did.
func TestTheLocalBackendIsBuiltAgainOverItsSlotsDirectory(t *testing.T) {
	t.Parallel()
	trust := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(trust, 0o700); err != nil {
		t.Fatal(err)
	}
	splitter, err := edge.NewSplitter(core.SystemClock{}, nil, edge.Target{Revision: "bootstrap", URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycleFlags{deploy: "local", localProgram: launcherPath}
	build := func() {
		t.Helper()
		if _, err := l.deployBackend("acme-app", splitter, trust, core.SystemClock{}, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatalf("deployBackend: %v", err)
		}
	}
	build()
	slots := filepath.Join(trust, localSlotsRecord)
	if err := os.Mkdir(slots, 0o700); err != nil {
		t.Fatal(err)
	}
	// What a crash while a record was written leaves.
	if err := os.WriteFile(filepath.Join(slots, "blue.json.tmp"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	build()
}
