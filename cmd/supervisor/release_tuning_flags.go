package main

import (
	"flag"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
)

// releaseTuning are the release monitor's window and sample floor. The
// defaults are the documented starting points; only the trusted simulation
// may judge on a shorter window or fewer samples, so a production factory
// cannot weaken its rollback rule from the command line.
type releaseTuning struct {
	window     time.Duration
	minSamples int64
}

func (t *releaseTuning) register(fs *flag.FlagSet) {
	fs.DurationVar(&t.window, "release-window", release.DefaultWindow, "length of one release observation window; shorter than the default only in the simulation profile")
	fs.Int64Var(&t.minSamples, "release-min-samples", release.DefaultMinSamples, "responses a window needs before its error rate counts; fewer than the default only in the simulation profile")
}

// monitor returns the rollback rule's window and sample floor for a
// factory in the given profile.
func (t *releaseTuning) monitor(sim bool) (release.MonitorConfig, error) {
	if t.window <= 0 || t.minSamples < 1 {
		return release.MonitorConfig{}, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "--release-window and --release-min-samples must be positive"))
	}
	if !sim && (t.window < release.DefaultWindow || t.minSamples < release.DefaultMinSamples) {
		return release.MonitorConfig{}, stages.Misconfigured(core.NewError(core.CodeInvalidArgument,
			"a shorter --release-window or a lower --release-min-samples is only for the simulation profile"))
	}
	return release.MonitorConfig{Window: t.window, MinSamples: t.minSamples}, nil
}
