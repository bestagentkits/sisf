package main

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
)

// runStages runs a round of the stages every interval until ctx ends. A
// failure is logged, through the redacting logger, and retried next round.
func runStages(ctx context.Context, st *stages.Stages, l *lifecycleFlags, clock core.Clock, log *slog.Logger) {
	for {
		res := st.Pass(ctx)
		if ctx.Err() == nil {
			logPass(log, res)
			l.sweepPrograms(ctx, log)
		}
		select {
		case <-ctx.Done():
			return
		case <-clock.After(l.interval):
		}
	}
}

// logPass logs what one round of the stages did: every failure, every
// candidate built, every QA verdict with its reason, and every release
// that ended, with its result and, when it did not go ahead, its code and
// reason.
func logPass(log *slog.Logger, res stages.PassResult) {
	for _, err := range res.Errors() {
		log.Warn("a lifecycle stage failed", "code", string(core.CodeOf(err)), "error", err.Error())
	}
	for _, issue := range res.Build.Submitted {
		log.Info("candidate built and submitted", "issue", issue.String())
	}
	for _, issue := range res.Build.Unbuildable {
		log.Info("patch refused by the builder; the run's next attempt follows", "issue", issue.String())
	}
	for _, issue := range res.Build.Held {
		log.Warn("candidate build held: it failed repeatedly on GitHub, the image builder or the configuration; no attempt was spent and it waits for the Owner's retry of the issue", "issue", issue.String())
	}
	for _, issue := range res.QA.Passed {
		log.Info("candidate passed QA", "issue", issue.String())
	}
	for _, issue := range res.QA.Failed {
		log.Info("candidate failed QA; the run's next attempt follows", "issue", issue.String(), "reason", res.QA.Reasons[issue])
	}
	for _, issue := range res.QA.Held {
		log.Warn("candidate held after QA: the issue waits in QAEvaluation for the Owner", "issue", issue.String(), "reason", res.QA.Reasons[issue])
	}
	issues := slices.SortedFunc(maps.Keys(res.Release.Outcomes), func(a, b core.IssueRef) int { return strings.Compare(a.String(), b.String()) })
	for _, issue := range issues {
		logRelease(log, issue, res.Release.Outcomes[issue])
	}
}

// releaseMessages say what each release result means for the issue.
var releaseMessages = map[release.Result]string{
	release.ResultHeld:          "release held before anything was deployed: the issue waits in ReleaseCandidate for the Owner's retry",
	release.ResultRefused:       "release refused before anything was deployed: the issue is blocked and the Owner's retry returns it to Ready for a new run",
	release.ResultBlocked:       "release undone: the last known good release serves again and the issue is blocked until the Owner's retry returns it to Ready",
	release.ResultInterrupted:   "release stopped after its rollout started: the issue is left where the state says, and the supervisor's recovery restores the last known good release and blocks it",
	release.ResultRestoreFailed: "release could not be undone: the last known good release is not observed running again; the supervisor tries again on every pass, and a person must look",
}

// logRelease logs one release that ended. A release that did not go ahead
// is logged once, when it ends: a held one is not tried again until the
// Owner's retry starts it over, and a refused one blocks its issue. A
// refusal whose block could not be written yet is logged on every pass
// until it is.
func logRelease(log *slog.Logger, issue core.IssueRef, out release.Outcome) {
	if out.Result == release.ResultClosed {
		log.Info("release closed: the verified release serves the application", "issue", issue.String(), "release", string(out.Release))
		return
	}
	if out.State == core.StateVerified && out.Waits != "" {
		log.Warn("verified release not finished yet: the issue stays Verified and the supervisor finishes it on a later pass",
			"issue", issue.String(), "waits", out.Waits, "release", string(out.Release))
		return
	}
	msg, ok := releaseMessages[out.Result]
	if !ok {
		msg = "release ended without closing the issue"
	}
	code := ""
	if c, _, found := strings.Cut(out.Reason, ": "); found && core.ErrorCode(c).Valid() {
		code = c
	}
	if out.Result == release.ResultRefused && out.State == core.StateReleaseCandidate {
		msg = "release refused before anything was deployed, and the issue could not be blocked yet: the next pass tries again"
	}
	log.Warn(msg, "issue", issue.String(), "result", string(out.Result), "state", string(out.State), "cause", string(out.Cause),
		"code", code, "reason", out.Reason, "release", string(out.Release))
}

// startLifecycle builds the stages when the flags ask for them, adds the
// Owner rollback to opts and returns the loop to run; nil when the
// lifecycle is off.
func (l *lifecycleFlags) startLifecycle(ctx context.Context, d lifecycleDeps, opts *supervisor.Options) (func(context.Context), error) {
	if !l.enabled() {
		return nil, nil
	}
	st, err := l.buildLifecycle(ctx, d)
	if err != nil {
		return nil, err
	}
	opts.Rollback = st.Pipeline.OwnerRollback
	opts.HeldBuilds = heldBuilds(d.stop.store)
	return func(ctx context.Context) {
		// A supervisor that stopped between a verification and the write of
		// its LKG left a Verified record whose release the LKG does not name
		// yet: it is recorded first, so adoption starts what the record says.
		if lkgs := st.Release.RecordVerifiedLKGs(ctx); ctx.Err() == nil {
			logPass(d.log, stages.PassResult{Release: lkgs})
		}
		if l.adopter != nil && !l.adopter.until(ctx, l, d.clock, d.log) {
			return
		}
		// A release a supervisor that stopped left in Canary or Monitoring
		// is undone before anything else runs: the last known good release,
		// which adoption put back under the local backend, is restored
		// through the backend and the issue blocked.
		recovered := st.Release.Recover(ctx)
		if ctx.Err() == nil {
			logPass(d.log, stages.PassResult{Release: recovered})
		}
		runStages(ctx, st, l, d.clock, d.log)
	}, nil
}

// getenv adapts lookupEnv to the getenv the process logger reads.
func getenv(lookupEnv func(string) (string, bool)) func(string) string {
	return func(name string) string {
		v, _ := lookupEnv(name)
		return v
	}
}
