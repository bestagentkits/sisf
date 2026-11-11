package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The store keeps the last known good program and the programs of
// candidates still on their way through QA and release, and nothing else;
// when what is needed cannot be read, nothing is removed.
func TestProgramsThatAreNeitherLastKnownGoodNorInFlightAreRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := trustStore(t)
	lkg, inFlight, old := exported(t, store, "last known good"), exported(t, store, "in flight"), exported(t, store, "rolled back")
	kept := func(d core.Digest) bool {
		_, err := store.Path(d)
		return err == nil
	}
	r := &programRetention{
		store:    store,
		lkg:      func(context.Context) (core.Digest, error) { return "", errors.New("the trust volume cannot be read") },
		inFlight: func(context.Context) ([]core.Digest, error) { return []core.Digest{inFlight}, nil },
	}
	if _, err := r.sweep(ctx); err == nil || !kept(old) || !kept(lkg) {
		t.Fatalf("an unreadable last known good release: %v; programs were removed", err)
	}
	r.lkg = func(context.Context) (core.Digest, error) { return lkg, nil }
	r.inFlight = func(context.Context) ([]core.Digest, error) { return nil, errors.New("the state store is down") }
	if _, err := r.sweep(ctx); err == nil || !kept(old) || !kept(inFlight) {
		t.Fatalf("unreadable records: %v; programs were removed", err)
	}
	r.inFlight = func(context.Context) ([]core.Digest, error) { return []core.Digest{inFlight}, nil }
	removed, err := r.sweep(ctx)
	if err != nil || len(removed) != 1 || !kept(lkg) || !kept(inFlight) || kept(old) {
		t.Errorf("removed %v (%v); last known good kept %t, in flight kept %t, rolled back kept %t", removed, err, kept(lkg), kept(inFlight), kept(old))
	}
}

// A candidate's program is in flight from its build until its release
// ended: closed, blocked or sent back to development.
func TestTheProgramsInFlightAreThoseOfCandidatesBetweenBuildAndTheEndOfTheirRelease(t *testing.T) {
	t.Parallel()
	digest := func(s string) core.Digest { return digestOfText(s) }
	records := []core.StateRecord{
		{State: core.StateQAEvaluation, CandidateDigest: digest("in QA")},
		{State: core.StateReleaseCandidate, CandidateDigest: digest("waiting for release")},
		{State: core.StateCanary, CandidateDigest: digest("rolling out"), ReleaseDigest: core.ReleaseDigest(digest("rolling out"))},
		{State: core.StateMonitoring, ReleaseDigest: core.ReleaseDigest(digest("watched"))},
		{State: core.StateVerified, ReleaseDigest: core.ReleaseDigest(digest("verified, not yet landed"))},
		{State: core.StateDeveloping, CandidateDigest: digest("failed QA")},
		{State: core.StateBlocked, CandidateDigest: digest("rolled back"), ReleaseDigest: core.ReleaseDigest(digest("rolled back"))},
		{State: core.StateClosed, ReleaseDigest: core.ReleaseDigest(digest("closed"))},
	}
	got := inFlightDigests(records)
	want := []core.Digest{digest("in QA"), digest("waiting for release"), digest("rolling out"), digest("watched"), digest("verified, not yet landed")}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("in flight: %v, want %v", got, want)
	}
	if !slices.Equal(inFlightStates, []core.State{core.StateQAEvaluation, core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring, core.StateVerified}) {
		t.Errorf("the states read are %v", inFlightStates)
	}
}
