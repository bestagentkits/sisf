//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
)

// More ways the run learns early that waiting longer only burns the
// deadline and the spend:
//   - QA held a candidate: a held verdict is final for the run, the issue
//     waits in QAEvaluation for a person and the lifecycle has no step out;
//   - the supervisor held the release, and the issue waits in
//     ReleaseCandidate until the Owner acts, or refused it, and the issue
//     is blocked until the Owner retries it; nothing is tried again;
//   - the journal has not moved for longer than the state can take: the
//     coding attempts of one Developing visit by the manifest's
//     run_timeout each (an attempt that fails before QA starts the next in
//     place, with no journal row), an evaluation by one candidate build and
//     the judgment, and a release candidate by a few rounds, since the
//     supervisor starts its release in the round QA passed it. Canary and
//     Monitoring wait out observation windows and holds by design and are
//     bounded by the run's deadline only.

const (
	// candidateBuildTimeout is the supervisor's default limit of one
	// candidate build (cmd/supervisor, --build-timeout); `sisf up` keeps it.
	candidateBuildTimeout = 20 * time.Minute
	// stallSlack covers the rounds of the units around a state's own work.
	stallSlack = 10 * time.Minute
)

// stallBounds says how long the run may stay in a state with no journal
// movement before it counts as stalled. States not named are not bounded.
func stallBounds(maxAttempts int, runTimeout time.Duration) map[string]time.Duration {
	return map[string]time.Duration{
		"Developing":   time.Duration(max(maxAttempts, 1))*runTimeout + stallSlack,
		"QAEvaluation": candidateBuildTimeout + agentjudge.DefaultTimeout + stallSlack,
		// The release starts in the round QA passed the candidate; opening
		// its pull request on GitHub comes first.
		"ReleaseCandidate": stallSlack,
	}
}

// stalled says why the run is stuck in the journal's last state at now, or
// "" while it may still move.
func stalled(journal []stateVisit, now time.Time, bounds map[string]time.Duration) string {
	if len(journal) == 0 {
		return ""
	}
	last := journal[len(journal)-1]
	bound, ok := bounds[last.State]
	if !ok || last.At.IsZero() {
		return ""
	}
	if in := now.Sub(last.At); in > bound {
		return fmt.Sprintf("the journal has not moved for %s in %s, longer than the %s the state can take",
			in.Round(time.Second), last.State, bound)
	}
	return ""
}

// heldQAVerdict says which QA verdict of the run was held and why, or ""
// when none was.
func heldQAVerdict(records map[string][]byte) string {
	for _, key := range sortedKeys(records) {
		var r qaRecord
		if json.Unmarshal(records[key], &r) == nil && r.Verdict == "held" {
			return fmt.Sprintf("QA held the candidate, and the issue waits in QAEvaluation for the Owner (%s)", describeQARecord(key, r))
		}
	}
	return ""
}

// heldRelease says which release of the run the supervisor held or
// refused and why, or "" when none was.
func heldRelease(records map[string][]byte) string {
	for _, key := range sortedKeys(records) {
		var r releaseRecord
		if json.Unmarshal(records[key], &r) == nil && (r.Result == "held" || r.Result == "refused") {
			return "the supervisor did not release the candidate, and the issue waits in " + r.State + " for the Owner (" + describeReleaseRecord(key, r) + ")"
		}
	}
	return ""
}

// qaStopped reads the run's QA and release records and its journal and
// says why the run cannot move on, or "".
func qaStopped(ctx context.Context, o *factoryObserver, issue, runID string, now time.Time) string {
	if runID != "" {
		if records, err := o.supervisorRecords(ctx, qaKeyPrefix+runID+"/"); err == nil {
			if why := heldQAVerdict(records); why != "" {
				return why
			}
		}
		if records, err := o.supervisorRecords(ctx, releaseKeyPrefix+runID+"/"); err == nil {
			if why := heldRelease(records); why != "" {
				return why
			}
		}
	}
	journal, err := o.journal(ctx, issue)
	if err != nil {
		return ""
	}
	return stalled(journal, now, stallBounds(o.maxAttempts, o.runTimeout))
}
