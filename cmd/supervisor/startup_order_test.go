package main

import (
	"context"
	"slices"
	"testing"
)

// orderedStartup is a startup that records the order of its steps; adopted
// is what adoption answers.
func orderedStartup(calls *[]string, adopted bool) startup {
	step := func(name string) func(context.Context) {
		return func(context.Context) { *calls = append(*calls, name) }
	}
	return startup{
		recordVerifiedLKGs: step("record-verified-lkgs"),
		adopt: func(context.Context) bool {
			*calls = append(*calls, "adopt")
			return adopted
		},
		recover: step("recover"),
		run:     step("run"),
	}
}

// The LKG of a release left Verified is recorded before the LKG is adopted,
// so the supervisor starts the verified release and not the one before it;
// releases left in flight are recovered only after adoption, and nothing
// runs when adoption gives up.
func TestTheVerifiedReleasesLKGIsRecordedBeforeAdoption(t *testing.T) {
	t.Parallel()
	var calls []string
	orderedStartup(&calls, true).start(context.Background())
	if want := []string{"record-verified-lkgs", "adopt", "recover", "run"}; !slices.Equal(calls, want) {
		t.Fatalf("startup ran %v, want %v", calls, want)
	}
	calls = nil
	orderedStartup(&calls, false).start(context.Background())
	if want := []string{"record-verified-lkgs", "adopt"}; !slices.Equal(calls, want) {
		t.Fatalf("startup after a failed adoption ran %v, want %v", calls, want)
	}
}
