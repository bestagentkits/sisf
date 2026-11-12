// Package fake is a scripted agent backend for tests and simulated installs.
// It runs nothing: each started run follows the next step scripted for its
// role and issue, on the injected clock, so a test decides exactly whether a
// run produces a patch, fails, hangs or crashes, and when.
//
// Scripts are kept per role, so a backend that serves the evaluator role can
// be scripted the same way as the coder; which roles a backend serves is part
// of its configuration, and a task for any other role is refused with
// NOT_SUPPORTED.
package fake

import (
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Outcome is how one scripted run ends.
type Outcome string

const (
	// OutcomePatch succeeds and leaves a patch artifact behind.
	OutcomePatch Outcome = "patch"
	// OutcomeFail ends the run as failed.
	OutcomeFail Outcome = "fail"
	// OutcomeHang keeps the run running until it is cancelled.
	OutcomeHang Outcome = "hang"
	// OutcomeCrash loses the run: once it is due, every question about it
	// answers UNAVAILABLE, as for an agent process that died without a report.
	OutcomeCrash Outcome = "crash"
)

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	return o == OutcomePatch || o == OutcomeFail || o == OutcomeHang || o == OutcomeCrash
}

// Step is the script of one run.
type Step struct {
	Outcome Outcome
	// After is how long the run works, on the backend's clock, before its
	// outcome shows. A hanging run ignores it.
	After time.Duration
	// Diff is the content of the patch an OutcomePatch run leaves.
	Diff string
	// Detail explains a failure.
	Detail string
}

// defaultDiff is the patch of a run scripted without one.
const defaultDiff = "--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n # app\n+scripted change\n"

// Patch scripts a run that succeeds after the given time with diff as its
// patch; an empty diff uses a small default patch.
func Patch(after time.Duration, diff string) Step {
	if diff == "" {
		diff = defaultDiff
	}
	return Step{Outcome: OutcomePatch, After: after, Diff: diff}
}

// Fail scripts a run that fails after the given time.
func Fail(after time.Duration, detail string) Step {
	if detail == "" {
		detail = "scripted failure"
	}
	return Step{Outcome: OutcomeFail, After: after, Detail: detail}
}

// Hang scripts a run that never finishes on its own.
func Hang() Step { return Step{Outcome: OutcomeHang} }

// Crash scripts a run that is lost after the given time.
func Crash(after time.Duration) Step { return Step{Outcome: OutcomeCrash, After: after} }

// validate rejects a step the backend could not follow.
func (s Step) validate() error {
	switch {
	case !s.Outcome.Valid():
		return core.NewError(core.CodeInvalidArgument, "a scripted step needs a known outcome")
	case s.After < 0:
		return core.NewError(core.CodeInvalidArgument, "a scripted step cannot finish before it starts")
	case s.Outcome == OutcomePatch && s.Diff == "":
		return core.NewError(core.CodeInvalidArgument, "a scripted patch needs content")
	}
	return nil
}
