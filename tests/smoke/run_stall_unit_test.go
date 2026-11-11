//go:build smoke

package smoke

import (
	"strings"
	"testing"
	"time"
)

// A held QA verdict is final for the run: the wait ends at once and says
// which attempt was held and why.
func TestTheRunStopsWaitingWhenQAHoldsTheCandidate(t *testing.T) {
	t.Parallel()
	failed := `{"verdict":"failed","reason":"hard gate \"observed_build_confidence\" failed","receipt":{"score":100,"checks":[` +
		`{"name":"tests_contract","kind":"hard_gate","passed":true},{"name":"observed_build_confidence","kind":"hard_gate","passed":false}]}}`
	held := `{"verdict":"held","reason":"attempt 3 of 3 failed QA","receipt":{"score":99,"checks":[{"name":"observed_build_confidence","kind":"hard_gate","passed":false}]}}`
	if why := heldQAVerdict(map[string][]byte{"qa/run-1/1": []byte(failed), "qa/run-1/2": []byte(failed)}); why != "" {
		t.Errorf("a failed verdict with attempts left stopped the wait: %s", why)
	}
	why := heldQAVerdict(map[string][]byte{"qa/run-1/1": []byte(failed), "qa/run-1/3": []byte(held)})
	for _, want := range []string{"QA held", "qa/run-1/3", "score 99", "failed hard gates observed_build_confidence", "attempt 3 of 3 failed QA"} {
		if !strings.Contains(why, want) {
			t.Errorf("the held verdict reads %q, want %q in it", why, want)
		}
	}
}

// The receipt evidence names the failed hard gates and the supervisor's
// reason, so a failed QA is diagnosable from the report alone.
func TestTheQAEvidenceNamesTheFailedGatesAndTheReason(t *testing.T) {
	t.Parallel()
	_, evidence := passingReceipt(map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"failed","reason":"hard gate \"secrets\" failed",` +
		`"receipt":{"score":100,"checks":[{"name":"secrets","kind":"hard_gate","passed":false},{"name":"clarity","kind":"rubric","passed":false}]}}`)})
	if !strings.Contains(evidence, "failed hard gates secrets,") || strings.Contains(evidence, "clarity") || !strings.Contains(evidence, `reason "hard gate \"secrets\" failed"`) {
		t.Errorf("the evidence reads %q", evidence)
	}
}

// The journal of the paid run that idled: each attempt moved on in
// minutes, and only the last evaluation, held, sat past what an evaluation
// can take. The release states wait out their windows and holds and are
// never counted as stalled.
func TestTheRunStopsWaitingWhenTheJournalStalls(t *testing.T) {
	t.Parallel()
	at := func(clock string) time.Time {
		v, err := time.Parse(time.DateTime, "2026-11-11 "+clock)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	journal := []stateVisit{
		{State: "Claimed", At: at("12:09:14")},
		{State: "Developing", At: at("12:09:14")},
		{State: "QAEvaluation", At: at("12:11:49")},
		{State: "Developing", At: at("12:12:53")},
		{State: "QAEvaluation", At: at("12:19:04")},
		{State: "Developing", At: at("12:20:22")},
		{State: "QAEvaluation", At: at("12:31:45")},
	}
	bounds := stallBounds(30 * time.Minute)
	for i := 1; i < len(journal); i++ {
		if why := stalled(journal[:i], journal[i].At, bounds); why != "" {
			t.Errorf("visit %d of %s counted as stalled before it moved on: %s", i, journal[i-1].State, why)
		}
	}
	if why := stalled(journal, at("12:31:45").Add(44*time.Minute), bounds); why != "" {
		t.Errorf("an evaluation within its bound counted as stalled: %s", why)
	}
	why := stalled(journal, at("13:49:00"), bounds)
	if !strings.Contains(why, "in QAEvaluation") || !strings.Contains(why, "1h17m15s") {
		t.Errorf("the held evaluation reads %q", why)
	}
	if why := stalled(append(journal, stateVisit{State: "Monitoring", At: at("13:00:00")}), at("13:45:00"), bounds); why != "" {
		t.Errorf("a 45-minute Monitoring hold counted as stalled: %s", why)
	}
	if why := stalled([]stateVisit{{State: "Developing", At: at("12:00:00")}}, at("12:41:00"), bounds); !strings.Contains(why, "Developing") {
		t.Errorf("a coding attempt past run_timeout reads %q", why)
	}
}
