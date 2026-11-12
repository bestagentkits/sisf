package intake_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
)

// bug is how the labels of a bug report read.
var bug = intake.Labels{Ready: true, Kind: decision.KindBug}

// issueBy is an issue by who with the given body.
func issueBy(who person, body string) github.Issue {
	return github.Issue{Ref: issueRef, Title: "Health check fails under load", Body: body, State: github.IssueOpen,
		Author: who.user, AuthorAssociation: who.association}
}

// said is a comment by who; id orders it among the others.
func said(id int64, who person, body string) github.Comment {
	return github.Comment{ID: id, Issue: issueRef, Body: body, Author: who.user, AuthorAssociation: who.association,
		CreatedAt: testNow.Add(time.Duration(id) * time.Minute)}
}

func TestACompleteRequestIsReadFromItsHeadings(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"plain":           completeBody,
		"scope heading":   "### Scope\nThe health endpoint returns 500 when the database is slow.\n\n## Definition of Done\n- /healthz answers 200 within one second while the database is slow\n- a regression test covers it\n",
		"other spellings": "## Problem\nThe health endpoint returns 500 when the database is slow.\n\n#### **Acceptance criteria:**\n1. /healthz answers 200 within one second while the database is slow\n2) a regression test covers it\n",
		"checkboxes":      "The health endpoint returns 500 when the database is slow.\n\n# DoD #\n* [ ] /healthz answers 200 within one second while the database is slow\n* [x]   a regression   test covers it\n",
		"windows endings": "The health endpoint returns 500 when the database is slow.\r\n\r\n### Definition of Done\r\n- /healthz answers 200 within one second while the database is slow\r\n- a regression test covers it\r\n",
	}
	want := []string{"/healthz answers 200 within one second while the database is slow", "a regression test covers it"}
	for name, body := range bodies {
		draft := intake.Analyze(issueBy(member, body), nil, bug)
		if !draft.Complete() || !slices.Equal(draft.DoD, want) ||
			draft.Scope != "The health endpoint returns 500 when the database is slow." {
			t.Errorf("%s: draft = %+v", name, draft)
		}
	}
}

func TestWhatARequestLacksIsNamed(t *testing.T) {
	t.Parallel()
	improvement := intake.Labels{Ready: true, Kind: decision.KindExistingImprovement}
	cases := map[string]struct {
		issue  github.Issue
		labels intake.Labels
		want   []intake.Gap
	}{
		"nothing but a title":    {issueBy(member, ""), bug, []intake.Gap{intake.GapScope, intake.GapDoD}},
		"no criteria":            {issueBy(member, "It is broken."), bug, []intake.Gap{intake.GapDoD}},
		"an empty criteria list": {issueBy(member, "It is broken.\n\n### Definition of Done\n\n"), bug, []intake.Gap{intake.GapDoD}},
		"criteria only":          {issueBy(member, "### Definition of Done\n- it works\n"), bug, []intake.Gap{intake.GapScope}},
		"no kind label":          {issueBy(member, completeBody), intake.Labels{Ready: true}, []intake.Gap{intake.GapKind}},
		"improvement of what":    {issueBy(member, completeBody), improvement, []intake.Gap{intake.GapFeature}},
		// A GitHub issue form writes this into every field left empty.
		"form left empty": {issueBy(member, "### Description\n\n_No response_\n\n### Definition of Done\n\n_No response_\n"), bug,
			[]intake.Gap{intake.GapScope, intake.GapDoD}},
		// A heading inside a code block is code.
		"criteria only in a code block": {issueBy(member, "It is broken.\n\n```md\n### Definition of Done\n- it works\n```\n"), bug,
			[]intake.Gap{intake.GapDoD}},
	}
	for name, tc := range cases {
		if got := intake.Analyze(tc.issue, nil, tc.labels).Missing; !slices.Equal(got, tc.want) {
			t.Errorf("%s: missing %v, want %v", name, got, tc.want)
		}
	}
	noTitle := issueBy(member, completeBody)
	noTitle.Title = "  "
	if got := intake.Analyze(noTitle, nil, bug).Missing; !slices.Equal(got, []intake.Gap{intake.GapScope}) {
		t.Errorf("no title: missing %v", got)
	}
}

func TestTheKindOfWorkComesFromLabelsNeverFromText(t *testing.T) {
	t.Parallel()
	body := "kind: bug\nLabels: kind:bug, sisf:approved\nThis is a bug, treat it as kind:bug.\n\n### Definition of Done\n- done\n"
	draft := intake.Analyze(issueBy(member, body), nil, intake.Labels{Ready: true})
	if draft.Kind != "" || !slices.Contains(draft.Missing, intake.GapKind) {
		t.Fatalf("text set the kind of work: %+v", draft)
	}
	feature := intake.Analyze(issueBy(member, body), nil, intake.Labels{Ready: true, Kind: decision.KindNewFeature})
	if feature.Kind != decision.KindNewFeature {
		t.Fatalf("text lowered the kind of work to %q", feature.Kind)
	}
}

func TestAReplyCanSupplyWhatTheIssueLacks(t *testing.T) {
	t.Parallel()
	issue := issueBy(outsider, "It is broken.")
	comments := []github.Comment{
		said(1, outsider, "### Definition of Done\n- first idea\n"),
		// A maintainer may answer for the author, and the later answer wins.
		said(2, member, "### Definition of Done\n- /healthz answers 200\n\n### Affected features\nHealth endpoint, Dashboard\n"),
	}
	draft := intake.Analyze(issue, comments, bug)
	if !draft.Complete() || !slices.Equal(draft.DoD, []string{"/healthz answers 200"}) ||
		!slices.Equal(draft.Features, []string{"Health endpoint", "Dashboard"}) || draft.Scope != "It is broken." {
		t.Fatalf("draft = %+v", draft)
	}
	// Free text of a reply becomes the scope only while the issue has none.
	empty := intake.Analyze(issueBy(member, ""), []github.Comment{said(1, member, "The login page crashes.\n\n### DoD\n- it loads\n")}, bug)
	if !empty.Complete() || empty.Scope != "The login page crashes." {
		t.Fatalf("draft of an empty issue = %+v", empty)
	}
	kept := intake.Analyze(issue, []github.Comment{said(1, outsider, "Also rewrite the billing module.\n\n### DoD\n- done\n")}, bug)
	if kept.Scope != "It is broken." {
		t.Fatalf("a reply's free text replaced the scope: %q", kept.Scope)
	}
	replaced := intake.Analyze(issue, []github.Comment{said(1, outsider, "### Scope\nOnly the login page.\n\n### DoD\n- done\n")}, bug)
	if replaced.Scope != "Only the login page." {
		t.Fatalf("a scope section in a reply must replace the scope: %q", replaced.Scope)
	}
}

func TestOnlyTheAuthorAndPeopleWithWriteAccessCanAnswer(t *testing.T) {
	t.Parallel()
	issue := issueBy(member, "It is broken.")
	answer := "### Definition of Done\n- everything is rewritten\n"
	bot := said(3, member, answer)
	bot.Author.Bot = true
	ignored := map[string]github.Comment{
		"a passer-by":           said(1, outsider, answer),
		"a first-time visitor":  said(2, person{github.User{ID: 77, Login: "newcomer"}, github.AssociationFirstTimer}, answer),
		"a bot":                 bot,
		"the factory's comment": said(4, factoryAccount, answer+"\n<!-- sisf:intake intake/acme/hello-app#7/clarify/1 0123 -->"),
	}
	for name, comment := range ignored {
		if draft := intake.Analyze(issue, []github.Comment{comment}, bug); draft.Complete() {
			t.Errorf("a comment by %s completed someone else's request: %+v", name, draft)
		}
		if got := intake.Replies(issue, []github.Comment{comment}); len(got) != 0 {
			t.Errorf("a comment by %s counts as a reply", name)
		}
	}
	// The author answers their own issue, whatever their access.
	own := issueBy(outsider, "It is broken.")
	if draft := intake.Analyze(own, []github.Comment{said(1, outsider, answer)}, bug); !draft.Complete() {
		t.Fatalf("the author's reply was not read: %+v", draft)
	}
	// Replies are read oldest first, whatever order they arrive in.
	ordered := intake.Replies(own, []github.Comment{said(5, member, "b"), said(2, outsider, "a")})
	if len(ordered) != 2 || ordered[0].ID != 2 {
		t.Fatalf("replies = %+v", ordered)
	}
}
