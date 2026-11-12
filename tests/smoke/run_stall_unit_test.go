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
	bounds := stallBounds(3, 30*time.Minute)
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
	// Attempts that fail before QA retry in place: two timed-out attempts
	// and a third still within its limit are a run at work.
	developing := []stateVisit{{State: "Developing", At: at("12:00:00")}}
	if why := stalled(developing, at("13:25:00"), bounds); why != "" {
		t.Errorf("a third coding attempt within its limit counted as stalled: %s", why)
	}
	if why := stalled(developing, at("13:41:00"), bounds); !strings.Contains(why, "Developing") {
		t.Errorf("coding past every attempt's run_timeout reads %q", why)
	}
}

// A held or refused release is final until the Owner acts: the wait ends
// at once with the supervisor's reason. A closed release, or none yet, is
// not a reason to stop.
func TestTheRunStopsWaitingWhenTheReleaseIsHeld(t *testing.T) {
	t.Parallel()
	held := `{"result":"held","state":"ReleaseCandidate","release":"sha256:aa","code":"RELEASE_HOLD","reason":"RELEASE_HOLD: real traffic is too low"}`
	why := heldRelease(map[string][]byte{"release/run-1/1": []byte(held)})
	for _, want := range []string{"release/run-1/1", "result held", "real traffic is too low"} {
		if !strings.Contains(why, want) {
			t.Errorf("the held release reads %q, want %q in it", why, want)
		}
	}
	refused := `{"result":"refused","state":"ReleaseCandidate","release":"sha256:aa","reason":"GOVERNANCE_REQUIRED: a governance change"}`
	if why := heldRelease(map[string][]byte{"release/run-1/1": []byte(refused)}); !strings.Contains(why, "result refused") {
		t.Errorf("a refused release reads %q", why)
	}
	for name, records := range map[string]map[string][]byte{
		"none":   nil,
		"closed": {"release/run-1/1": []byte(`{"result":"closed","state":"Closed","release":"sha256:aa"}`)},
	} {
		if why := heldRelease(records); why != "" {
			t.Errorf("%s stopped the wait: %s", name, why)
		}
	}
}

// A release candidate is released in the round QA passed it; one that sits
// in ReleaseCandidate past the slack of a few rounds was not released.
func TestTheRunStopsWaitingWhenTheCandidateIsNotReleased(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 11, 17, 47, 7, 0, time.UTC)
	journal := []stateVisit{{State: "QAEvaluation", At: start.Add(-36 * time.Second)}, {State: "ReleaseCandidate", At: start}}
	bounds := stallBounds(3, 30*time.Minute)
	if why := stalled(journal, start.Add(5*time.Minute), bounds); why != "" {
		t.Errorf("a candidate five minutes into its release counted as stalled: %s", why)
	}
	if why := stalled(journal, start.Add(96*time.Minute), bounds); !strings.Contains(why, "in ReleaseCandidate") {
		t.Errorf("a candidate never released reads %q", why)
	}
}
