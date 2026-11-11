package wiring

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxLoggedDetail bounds the detail of a failed attempt in the log.
const maxLoggedDetail = 500

// DispatcherTickLog returns the dispatcher's OnTick: it logs what each tick
// did, so a claimed issue that does not move on says why. Events (a claim,
// a failed attempt, a run that used every attempt) are logged as they
// happen; states that last (standing by for another instance, Ready issues
// held back, a failure that repeats every tick) are logged when they begin
// or change. Errors are logged by their typed messages, which carry no
// cause and so no output of a failed program; the process log redacts the
// secrets it knows of besides.
func DispatcherTickLog(log *slog.Logger) func(dispatcher.TickResult, error) {
	var (
		mu                 sync.Mutex
		standby            bool
		held, lastFailures string
	)
	return func(res dispatcher.TickResult, err error) {
		mu.Lock()
		defer mu.Unlock()
		failures := tickFailures(res, err)
		if failures != lastFailures && failures != "" {
			log.Warn("a dispatcher tick failed", "errors", failures)
		}
		lastFailures = failures
		if err != nil {
			return
		}
		if !res.Leader && !standby {
			log.Info("dispatcher standing by: another instance holds the dispatcher lease")
		}
		standby = !res.Leader
		if code := string(core.CodeOf(res.Held)); code != held {
			if code != "" {
				log.Info("Ready issues are held back", "code", code, "reason", res.Held.Error())
			}
			held = code
		}
		for _, issue := range res.Claimed {
			log.Info("issue claimed", "issue", issue.String())
		}
		for _, issue := range res.Resumed {
			log.Info("run taken over from the stored state", "issue", issue.String())
		}
		for _, f := range res.Failed {
			log.Warn("a coding attempt failed", "issue", f.Issue.String(), "run", string(f.RunID),
				"attempt", f.Attempt, "code", string(f.Code), "detail", truncateDetail(f.Detail))
		}
		for _, issue := range res.Exhausted {
			log.Error("a run used every attempt and stopped; the issue stays where it is until the Owner acts",
				"issue", issue.String())
		}
		for _, issue := range res.Succeeded {
			log.Info("coding attempt succeeded", "issue", issue.String())
		}
		for _, issue := range res.Lost {
			log.Warn("run lost to another holder or a lapsed lease", "issue", issue.String())
		}
	}
}

// tickFailures joins the store error and the per-run errors of a tick.
func tickFailures(res dispatcher.TickResult, err error) string {
	var parts []string
	if err != nil {
		parts = append(parts, err.Error())
	}
	for _, e := range res.Errors {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

func truncateDetail(s string) string {
	if len(s) <= maxLoggedDetail {
		return s
	}
	return s[:maxLoggedDetail] + "..."
}
