package wiring

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/github"
)

// An edit is recorded as the factory's own only when the issue read after
// it differs from the one read before by what the edit did: a person's
// change in between, in the same second, keeps it from being recorded.
func TestAFactoryEditExplainsOnlyWhatItDid(t *testing.T) {
	t.Parallel()
	issue := func(title string, state github.IssueState, labels ...string) github.Issue {
		is := github.Issue{Title: title, Body: "b", State: state}
		for _, l := range labels {
			is.Labels = append(is.Labels, github.Label{Name: l})
		}
		return is
	}
	before := issue("t", github.IssueOpen, "sisf:ready", "kind:bug")
	park := editIntent{add: []string{github.LabelNeedsInfo}}
	closeIt := editIntent{add: []string{"sisf:closed"}, remove: []string{"sisf:ready"}, close: true}
	cases := []struct {
		name   string
		intent editIntent
		after  github.Issue
		want   bool
	}{
		{"a comment", editIntent{}, before, true},
		{"a comment and a person's label", editIntent{}, issue("t", github.IssueOpen, "sisf:ready", "kind:bug", "kind:feature"), false},
		{"a comment and a person's retitle", editIntent{}, issue("new", github.IssueOpen, "sisf:ready", "kind:bug"), false},
		{"the parking label", park, issue("t", github.IssueOpen, "sisf:ready", "kind:bug", github.LabelNeedsInfo), true},
		{"the parking label and a person's removal", park, issue("t", github.IssueOpen, "sisf:ready", github.LabelNeedsInfo), false},
		{"closing", closeIt, issue("t", github.IssueClosed, "kind:bug", "sisf:closed"), true},
		{"a person closing", editIntent{}, issue("t", github.IssueClosed, "sisf:ready", "kind:bug"), false},
	}
	for _, tc := range cases {
		if got := tc.intent.explains(before, tc.after); got != tc.want {
			t.Errorf("%s: explains = %v, want %v", tc.name, got, tc.want)
		}
	}
}
