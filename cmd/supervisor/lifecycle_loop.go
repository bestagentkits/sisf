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
	release.ResultHeld:          "release held: the issue waits in ReleaseCandidate for the Owner",
	release.ResultRefused:       "release refused: the refusal is final for this candidate and it is not tried again; the issue waits in ReleaseCandidate until the Owner changes the policy in a governed update or closes the issue",
	release.ResultBlocked:       "release rolled back: the issue is blocked",
	release.ResultRestoreFailed: "release could not be undone: the last known good release is not observed running again, and a person must act",
}

// logRelease logs one release that ended. A release that did not go ahead
// is logged once, when it ends: a held one is not tried again until the
// Owner's retry starts it over, and a refused one never.
func logRelease(log *slog.Logger, issue core.IssueRef, out release.Outcome) {
	if out.Result == release.ResultClosed {
		log.Info("release closed: the verified release serves the application", "issue", issue.String(), "release", string(out.Release))
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
	log.Warn(msg, "issue", issue.String(), "result", string(out.Result), "state", string(out.State), "code", code,
		"reason", out.Reason, "release", string(out.Release))
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
		if l.adopter != nil && !l.adopter.until(ctx, l, d.clock, d.log) {
			return
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
