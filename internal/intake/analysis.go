package intake

import (
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Gap is one piece of information a request still lacks.
type Gap string

const (
	// GapKind: no kind label says what sort of work this is.
	GapKind Gap = "kind"
	// GapScope: nothing says what should change.
	GapScope Gap = "scope"
	// GapDoD: nothing says how to tell that the work is done.
	GapDoD Gap = "definition_of_done"
	// GapFeature: an improvement does not name the feature it improves.
	GapFeature Gap = "affected_feature"
)

// Draft is what an issue and its replies say about the work, read by fixed
// rules. The text in it is quoted, compared and hashed; it is never obeyed.
type Draft struct {
	Kind  decision.Kind
	Title string
	// Scope is the description of what should change.
	Scope string
	// DoD lists the criteria that tell the work is done.
	DoD []string
	// Features names the features the work touches.
	Features []string
	// Missing lists what has to be asked before a scope can be written.
	Missing []Gap
}

// Complete reports whether nothing has to be asked.
func (d Draft) Complete() bool { return len(d.Missing) == 0 }

// Analyze reads the issue and the replies that count and reports what the
// request says and what it still lacks.
//
// The kind comes from the labels alone. The body gives the scope, and its
// "Definition of Done" and "Affected features" sections give those. A reply
// can supply or replace a section by using the same heading; its other text
// fills the scope only while the issue has none. Later replies win.
func Analyze(issue github.Issue, comments []github.Comment, labels Labels) Draft {
	body := parseSections(issue.Body)
	draft := Draft{
		Kind:     labels.Kind,
		Title:    collapse(issue.Title),
		Scope:    normalizeText(strings.Join([]string{body.Preamble, body.Scope, body.Other}, "\n")),
		DoD:      listItems(body.DoD),
		Features: featureNames(body.Features),
	}
	for _, reply := range Replies(issue, comments) {
		said := parseSections(reply.Body)
		if scope := normalizeText(said.Scope); scope != "" {
			draft.Scope = scope
		} else if draft.Scope == "" {
			draft.Scope = normalizeText(said.Preamble)
		}
		if dod := listItems(said.DoD); len(dod) > 0 {
			draft.DoD = dod
		}
		if features := featureNames(said.Features); len(features) > 0 {
			draft.Features = features
		}
	}
	switch {
	case draft.Kind == "":
		draft.Missing = append(draft.Missing, GapKind)
	case draft.Kind == decision.KindExistingImprovement && len(draft.Features) == 0:
		draft.Missing = append(draft.Missing, GapFeature)
	}
	if draft.Title == "" || draft.Scope == "" {
		draft.Missing = append(draft.Missing, GapScope)
	}
	if len(draft.DoD) == 0 {
		draft.Missing = append(draft.Missing, GapDoD)
	}
	return draft
}

// featureNames reads a features section: list items or lines, each of which
// may hold several names separated by commas.
func featureNames(text string) []string {
	var names []string
	for _, item := range listItems(text) {
		for _, name := range strings.Split(item, ",") {
			if name = collapse(name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// Replies returns the comments that count as answers, oldest first.
//
// A comment counts when it was written by the author of the issue or by
// someone with write access to the repository. Comments of bots and the
// factory's own comments never count, and neither does a comment by anyone
// else: a passer-by cannot add to the scope of someone else's request.
func Replies(issue github.Issue, comments []github.Comment) []github.Comment {
	var out []github.Comment
	for _, c := range comments {
		byAuthor := issue.Author.ID != 0 && c.Author.ID == issue.Author.ID
		mayAnswer := byAuthor || c.AuthorAssociation.IsCollaborator()
		if c.Author.Bot || IsFactoryComment(c.Body) || !mayAnswer {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// latestReply returns the highest ID among the replies that count, or 0.
func latestReply(issue github.Issue, comments []github.Comment) int64 {
	var latest int64
	for _, reply := range Replies(issue, comments) {
		latest = max(latest, reply.ID)
	}
	return latest
}
