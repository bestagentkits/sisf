//go:build smoke

package smoke

import (
	"strings"
	"testing"
)

func TestOnlyAPassedSignedReceiptAtThePassMarkCounts(t *testing.T) {
	t.Parallel()
	signed := `"signature":{"key_id":"qa-1","algorithm":"ed25519","value":"c2ln"}`
	for name, tc := range map[string]struct {
		records map[string][]byte
		want    bool
	}{
		"none":            {map[string][]byte{}, false},
		"passed at 85":    {map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"passed","receipt":{"score":85,` + signed + `}}`)}, true},
		"passed at 84":    {map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"passed","receipt":{"score":84,` + signed + `}}`)}, false},
		"unsigned":        {map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"passed","receipt":{"score":95}}`)}, false},
		"failed":          {map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"failed","receipt":{"score":95,` + signed + `}}`)}, false},
		"a later attempt": {map[string][]byte{"qa/run-1/1": []byte(`{"verdict":"failed"}`), "qa/run-1/2": []byte(`{"verdict":"passed","receipt":{"score":90,` + signed + `}}`)}, true},
		"unreadable":      {map[string][]byte{"qa/run-1/1": []byte(`{`)}, false},
	} {
		if got, evidence := passingReceipt(tc.records); got != tc.want {
			t.Errorf("%s: %t, want %t (%s)", name, got, tc.want, evidence)
		}
	}
}

func TestOnlyAClosedReleaseCounts(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		record string
		want   bool
	}{
		"closed":      {`{"result":"closed","state":"Closed","release":"sha256:aa"}`, true},
		"held":        {`{"result":"held","state":"Canary","release":"sha256:aa"}`, false},
		"rolled back": {`{"result":"blocked","state":"Blocked","release":"sha256:aa"}`, false},
	} {
		if got, evidence := completedRelease(map[string][]byte{"release/run-1/1": []byte(tc.record)}); got != tc.want {
			t.Errorf("%s: %t, want %t (%s)", name, got, tc.want, evidence)
		}
	}
	if ok, _ := completedRelease(nil); ok {
		t.Error("no release record counted as a completed release")
	}
}

func TestTheJournalMustPassTheReleaseStatesInOrder(t *testing.T) {
	t.Parallel()
	visits := func(states ...string) []stateVisit {
		var out []stateVisit
		for _, s := range states {
			out = append(out, stateVisit{State: s})
		}
		return out
	}
	want := []string{"QAEvaluation", "ReleaseCandidate", "Verified", "Closed"}
	if !visitedInOrder(visits("Ready", "Developing", "QAEvaluation", "ReleaseCandidate", "Canary", "Monitoring", "Verified", "Closed"), want...) {
		t.Error("a full lifecycle was not recognised")
	}
	if visitedInOrder(visits("Ready", "Developing", "Closed"), want...) {
		t.Error("an issue closed without a release was taken for a release")
	}
}

func TestTheMergedDiffMustAddTheRequestedRoute(t *testing.T) {
	t.Parallel()
	added := strings.Join([]string{"@@ -1,3 +1,5 @@", ` mux.HandleFunc("GET /healthz", health)`, `+	mux.HandleFunc("GET /version", version)`}, "\n")
	if !addsRoute(added, versionRoute) {
		t.Error("an added route registration was missed")
	}
	removed := strings.Join([]string{"--- a/app.go", "+++ b/app.go", `-	mux.HandleFunc("GET /version", version)`, `+	// "/versioned" is planned`}, "\n")
	if addsRoute(removed, versionRoute) {
		t.Error("a removed route or a longer path was taken for the requested route")
	}
}

// The release evidence carries the supervisor's reason, so a held release
// reads without the install.
func TestTheReleaseEvidenceNamesTheReason(t *testing.T) {
	t.Parallel()
	_, evidence := completedRelease(map[string][]byte{"release/run-1/1": []byte(
		`{"result":"held","state":"ReleaseCandidate","release":"sha256:aa","code":"RELEASE_HOLD","reason":"RELEASE_HOLD: real traffic is too low"}`)})
	for _, want := range []string{"result held", "code RELEASE_HOLD", `reason "RELEASE_HOLD: real traffic is too low"`} {
		if !strings.Contains(evidence, want) {
			t.Errorf("the release evidence reads %q, want %q in it", evidence, want)
		}
	}
}
