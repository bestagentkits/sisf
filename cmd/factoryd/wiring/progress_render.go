package wiring

import (
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// progressFacts is what the issue's progress comment is rendered from, all
// of it read from the operational store: states, numbers, gate names,
// commits and the supervisor's fixed reasons, never an agent's or a
// person's text.
type progressFacts struct {
	record core.StateRecord
	// run is the dispatcher's record of the run; nil when it has none.
	run      *dispatcher.RunRecord
	notes    []handoff.QANote
	hold     *handoff.QAHold
	pr       *handoff.CandidatePR
	restored core.ReleaseDigest
}

// releaseSteps are the release states in the order a release reaches them.
var releaseSteps = []core.State{core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring, core.StateVerified, core.StateClosed}

// renderProgress is the body of the progress comment: where the issue is
// now and every checkpoint it passed, one line each.
func renderProgress(f progressFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Factory progress\n\n**Now:** `%s`", f.record.State)
	if f.record.RunID != "" {
		fmt.Fprintf(&b, " · run `%s`", f.record.RunID)
	}
	b.WriteString("\n\n")
	line := func(format string, args ...any) { fmt.Fprintf(&b, "- "+format+"\n", args...) }
	if f.record.RunID != "" {
		line("Claimed by run `%s`.", f.record.RunID)
	}
	if f.run != nil {
		for _, failure := range f.run.Failures {
			line("Coding attempt %d failed (`%s`).", failure.Attempt, failure.Code)
		}
	}
	for _, n := range f.notes {
		line("QA attempt %d of %d: %s.", n.Attempt, n.MaxAttempts, noteText(n))
	}
	if f.hold != nil {
		line("**Held:** %s", f.hold.Summary())
	}
	if f.pr != nil {
		line("Candidate pull request: #%d (%s).", f.pr.Number, f.pr.URL)
	}
	if reached := reachedRelease(f.record.State); reached != "" {
		line("Release: %s.", reached)
	}
	if f.restored != "" {
		line("**Rolled back** to the last known good release `%s`; the factory will not merge this candidate.", f.restored)
	}
	if f.pr != nil {
		if text := landingText(*f.pr); text != "" {
			line("%s", text)
		}
	}
	if f.record.State == core.StateClosed {
		merged := "no candidate pull request was merged"
		if f.pr != nil && f.pr.State == handoff.LandingMerged {
			merged = "the change is merged"
		} else if f.pr != nil {
			merged = "the change is **not** merged"
		}
		line("Closed: the verified release is running and its observation window passed; %s.", merged)
	}
	return b.String()
}

func noteText(n handoff.QANote) string {
	switch {
	case !n.Built:
		return n.Verdict + ", the patch could not be built"
	case !n.Receipt:
		return n.Verdict + ", no signed receipt was issued"
	}
	text := fmt.Sprintf("%s, score %d against the pass mark %d", n.Verdict, n.Score, n.PassScore)
	if len(n.FailedGates) > 0 {
		text += "; failed hard gates: `" + strings.Join(n.FailedGates, "`, `") + "`"
	}
	return text
}

// reachedRelease lists the release states the issue reached, "" before
// the first.
func reachedRelease(s core.State) string {
	for i, step := range releaseSteps {
		if step != s {
			continue
		}
		names := make([]string, 0, i+1)
		for _, reached := range releaseSteps[:i+1] {
			if reached != core.StateClosed {
				names = append(names, string(reached))
			}
		}
		return strings.Join(names, " → ")
	}
	return ""
}

// landingText says what became of the candidate's pull request.
func landingText(pr handoff.CandidatePR) string {
	switch pr.State {
	case handoff.LandingPending:
		return fmt.Sprintf("Merging #%d into `%s` now that the release is verified.", pr.Number, pr.Base)
	case handoff.LandingMerged:
		if pr.MergeCommit != "" {
			return fmt.Sprintf("**Merged** #%d into `%s` as `%s`.", pr.Number, pr.Base, pr.MergeCommit)
		}
		return fmt.Sprintf("**Merged** #%d into `%s`.", pr.Number, pr.Base)
	case handoff.LandingLeftOpen:
		return fmt.Sprintf("**Not merged:** #%d is left for the Owner: %s.", pr.Number, oneLine(pr.Reason))
	}
	return ""
}
