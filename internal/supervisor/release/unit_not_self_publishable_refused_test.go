package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A unit that release.units does not list is published by a person, never
// by the pipeline: the release is refused before anything is staged.
func TestAReleaseOfAUnitTheFactoryMayNotPublishIsRefused(t *testing.T) {
	t.Parallel()
	for _, units := range map[string][]string{
		"release.units lists other units": {"control", "dispatcher"},
		"release.units is empty":          {},
	} {
		w := newWorld(t)
		w.cfg.SelfPublishable = units
		issue, run := issueRef(5), core.RunID("run-5")
		w.seedReleaseCandidate(issue, run, firstBuild)

		out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
		requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
	}
}

func TestAPipelinePublishesTheUnitsReleaseUnitsListsByDefault(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.pipeline()
	if got := p.cfg.SelfPublishable; len(got) != len(DefaultSelfPublishable()) {
		t.Fatalf("units published by default = %v, want %v", got, DefaultSelfPublishable())
	}
	for _, unit := range []string{"supervisor", "qa-runner", "edge", "state", "artifacts", "", "App"} {
		cfg := w.cfg
		cfg.SelfPublishable = []string{"app", unit}
		if _, err := New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("a pipeline that publishes %q by itself was built: %v", unit, err)
		}
	}
}
