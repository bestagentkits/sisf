package wiring_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The dispatcher's log says why a claimed issue does not move on: an
// attempt that failed to start, a run that used every attempt, Ready issues
// held back, a store it cannot reach, or standing by for another instance.
// A state that persists is logged when it begins, not on every tick.
func TestTheDispatcherLogSaysWhyAClaimedIssueDoesNotMoveOn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	onTick := wiring.DispatcherTickLog(slog.New(slog.NewTextHandler(&buf, nil)))
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	failed := dispatcher.FailedAttempt{Issue: issue, RunID: "run-0001", AttemptFailure: dispatcher.AttemptFailure{
		Attempt: 3, Code: core.CodeInvalidArgument, Detail: "the agent did not start: INVALID_ARGUMENT: the workspace source is not configured",
	}}
	held := core.NewError(core.CodeBudgetUnconfirmed, "the Owner has not confirmed the budget")

	onTick(dispatcher.TickResult{}, nil)
	onTick(dispatcher.TickResult{}, nil)
	onTick(dispatcher.TickResult{Leader: true, Claimed: []core.IssueRef{issue}, Held: held}, nil)
	onTick(dispatcher.TickResult{Leader: true, Held: held}, nil)
	onTick(dispatcher.TickResult{Leader: true, Failed: []dispatcher.FailedAttempt{failed}, Exhausted: []core.IssueRef{issue}}, nil)
	onTick(dispatcher.TickResult{}, core.NewError(core.CodeUnavailable, "the state store cannot be reached"))

	out := buf.String()
	for _, want := range []string{
		"standing by", "issue claimed", "acme/app#7", "BUDGET_UNCONFIRMED",
		"a coding attempt failed", "attempt=3", "the workspace source is not configured",
		"a run used every attempt", "the state store cannot be reached",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "standing by"); n != 1 {
		t.Errorf("standing by is logged %d times, want once while it lasts:\n%s", n, out)
	}
	if n := strings.Count(out, "held back"); n != 1 {
		t.Errorf("the hold is logged %d times, want once while it lasts:\n%s", n, out)
	}
}

// A long detail of a failed attempt is cut on a character boundary, so the
// log never holds half of a multi-byte character.
func TestTheDispatcherLogCutsALongDetailOnACharacterBoundary(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	onTick := wiring.DispatcherTickLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	// One byte, then three-byte characters: byte 500 falls inside one.
	detail := "x" + strings.Repeat("世", 300)
	onTick(dispatcher.TickResult{Leader: true, Failed: []dispatcher.FailedAttempt{{
		Issue: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-0001",
		AttemptFailure: dispatcher.AttemptFailure{Attempt: 1, Code: core.CodeInternal, Detail: detail},
	}}}, nil)
	var line struct{ Detail string }
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	logged, ok := strings.CutSuffix(line.Detail, "...")
	if !ok || !utf8.ValidString(logged) || len(logged) > 500 || len(logged) < 497 || !strings.HasPrefix(detail, logged) {
		t.Fatalf("the logged detail is not cut on a character boundary near 500 bytes: %d bytes, %q", len(logged), line.Detail)
	}
}
