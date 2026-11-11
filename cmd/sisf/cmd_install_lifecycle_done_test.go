package main

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/install"
)

// A lifecycle command says what it did: a Docker install that never
// started its project removed nothing, and volumes of its project's name
// that another install made are that install's, not this one's.
func TestALifecycleCommandSaysWhatItDidNotWhatItWouldHaveDone(t *testing.T) {
	t.Parallel()
	const dir = "/srv/factory"
	never := install.PriorState{NeverStarted: true}
	foreign := install.PriorState{NeverStarted: true, ForeignData: "the install in /srv/other"}
	cases := []struct {
		name    string
		volumes bool
		prior   install.PriorState
		want    []string
		notWant string
	}{
		{"down", true, never, []string{"was never started", "nothing was removed"}, "its data is removed"},
		{"down", true, foreign, []string{"was never started", "nothing was removed", "belong to the install in /srv/other"}, "its data is removed"},
		{"down", false, never, []string{"was never started", "nothing to stop"}, "is down"},
		{"stop", false, never, []string{"was never started", "nothing to stop"}, "is stopped"},
		{"stop", false, install.PriorState{}, []string{"was not running"}, "is stopped"},
		{"down", true, install.PriorState{}, []string{"was not running", "its data is removed"}, "is down and"},
		{"down", true, install.PriorState{Running: true}, []string{"is down and its data is removed"}, "not running"},
		{"stop", false, install.PriorState{Running: true}, []string{"is stopped"}, "not running"},
	}
	for _, tc := range cases {
		got := lifecycleDone(tc.name, dir, tc.volumes, tc.prior)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s (-v %v, %+v) = %q, want it to say %q", tc.name, tc.volumes, tc.prior, got, w)
			}
		}
		if strings.Contains(got, tc.notWant) {
			t.Errorf("%s (-v %v, %+v) = %q, must not say %q", tc.name, tc.volumes, tc.prior, got, tc.notWant)
		}
	}
}
