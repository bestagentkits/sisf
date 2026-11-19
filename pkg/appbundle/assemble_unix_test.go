//go:build unix

package appbundle

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func TestAssembleRefusesSpecialFilesInTheRepository(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		fifo   string
		source *managedapp.Source
		want   string
	}{
		"fifo inside a source dir": {fifo: "plugins/custom/pipe", want: "not a plain file"},
		"fifo as a source":         {fifo: "pipe", source: &managedapp.Source{From: "pipe", To: "pipe"}, want: "not a plain file"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			if err := syscall.Mkfifo(filepath.Join(s.in.RepoDir, filepath.FromSlash(tc.fifo)), 0o644); err != nil {
				t.Skipf("mkfifo unavailable: %v", err)
			}
			if tc.source != nil {
				s.in.Bundle.Sources = append(s.in.Bundle.Sources, *tc.source)
			}
			assertRefused(t, s, tc.want)
		})
	}
}
