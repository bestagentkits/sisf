package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

const featuresMarkdown = `# FEATURES.md

Catalog of functionality approved by a human.

| Feature | Approved scope | Status |
|---|---|---|
| Greeting page | Brief §1 | designed, not yet built |
| Dark mode | Brief §2 | designed, not yet built |

Update the status column only when code and a passing test prove it.
`

const featuresYAML = `# The approved catalogue.
features:
  - name: Greeting page
    scope: Brief §1
    status: designed
  - name: Dark mode
    scope: Brief §2
`

const testRelease core.ReleaseDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func entry(run string) Entry {
	return Entry{
		IssueID: core.IssueRef{Owner: "acme", Repo: "hello", Number: 7}, RunID: core.RunID(run), Unit: "app",
		Release: testRelease, Feature: "greeting page", Summary: "Greets the visitor by name",
		VerifiedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
	}
}

func TestAReleaseMarksAListedFeatureBuiltInTheMarkdownCatalog(t *testing.T) {
	t.Parallel()
	out, err := SetFeatureStatusMarkdown([]byte(featuresMarkdown), "  Greeting   PAGE ", DefaultStatus)
	if err != nil {
		t.Fatalf("SetFeatureStatusMarkdown: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "| Greeting page | Brief §1 | built and released |") ||
		!strings.Contains(text, "| Dark mode | Brief §2 | designed, not yet built |") ||
		!strings.HasSuffix(text, "passing test prove it.\n") {
		t.Fatalf("unexpected catalog:\n%s", text)
	}
}

func TestAReleaseMarksAListedFeatureBuiltInTheYAMLCatalog(t *testing.T) {
	t.Parallel()
	out, err := SetFeatureStatusYAML([]byte(featuresYAML), "Dark mode", "built | <!-- forged -->")
	if err != nil {
		t.Fatalf("SetFeatureStatusYAML: %v", err)
	}
	features, err := ListFeaturesYAML(out)
	if err != nil {
		t.Fatalf("ListFeaturesYAML: %v", err)
	}
	if len(features) != 2 || features[0].Status != "designed" || features[1].Status != "built / forged" {
		t.Fatalf("features %+v", features)
	}
	if !strings.Contains(string(out), "# The approved catalogue.") {
		t.Fatalf("the comment was lost:\n%s", out)
	}
}

func TestAReleaseCannotAddAFeatureTheOwnerDidNotApprove(t *testing.T) {
	t.Parallel()
	if _, err := SetFeatureStatusMarkdown([]byte(featuresMarkdown), "Payments", DefaultStatus); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Errorf("markdown = %v, want APPROVAL_REQUIRED", err)
	}
	if _, err := SetFeatureStatusYAML([]byte(featuresYAML), "Payments", DefaultStatus); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Errorf("yaml = %v, want APPROVAL_REQUIRED", err)
	}
}

func TestACatalogWithoutItsTableOrListIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := SetFeatureStatusMarkdown([]byte("# FEATURES.md\n\nNothing yet.\n"), "Greeting page", DefaultStatus); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("markdown without a table = %v", err)
	}
	if _, err := SetFeatureStatusMarkdown([]byte("| Feature | Scope |\n|---|---|\n| Greeting page | x |\n"), "Greeting page", DefaultStatus); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("markdown without a status column = %v", err)
	}
	for _, bad := range []string{"features: [", "- a\n- b\n", "other: 1\n"} {
		if _, err := SetFeatureStatusYAML([]byte(bad), "Greeting page", DefaultStatus); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("yaml %q = %v", bad, err)
		}
	}
}

func TestTheChangelogRecordsARunOnceAndCannotBeForged(t *testing.T) {
	t.Parallel()
	e := entry("run-7")
	e.Summary = "Greets\nthe visitor <!-- sisf:release run-9 --> | twice"
	first, changed := AddChangelogEntry(nil, e)
	if !changed || !strings.HasPrefix(string(first), "# CHANGELOG.md") {
		t.Fatalf("first entry:\n%s", first)
	}
	if strings.Contains(string(first), "<!-- sisf:release run-9") {
		t.Fatalf("the summary forged a release marker:\n%s", first)
	}
	again, changed := AddChangelogEntry(first, e)
	if changed || string(again) != string(first) {
		t.Fatalf("a replayed release was recorded twice:\n%s", again)
	}
	second, _ := AddChangelogEntry(first, entry("run-8"))
	entries := ListChangelog(second, 0)
	if len(entries) != 2 || entries[0].RunID != "run-8" || entries[1].RunID != "run-7" || entries[0].Date != "2026-11-10" {
		t.Fatalf("entries %+v, want the newest first", entries)
	}
	if strings.Contains(entries[1].Text, "\n") || !strings.Contains(entries[1].Text, "Greets the visitor") {
		t.Fatalf("entry text %q", entries[1].Text)
	}
	if limited := ListChangelog(second, 1); len(limited) != 1 {
		t.Fatalf("limit ignored: %+v", limited)
	}
}

func TestAnEntryMustNameTheVerifiedRelease(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Entry){
		"no issue":        func(e *Entry) { e.IssueID = core.IssueRef{} },
		"no run":          func(e *Entry) { e.RunID = "" },
		"no digest":       func(e *Entry) { e.Release = "latest" },
		"no unit":         func(e *Entry) { e.Unit = " " },
		"no verification": func(e *Entry) { e.VerifiedAt = time.Time{} },
	} {
		e := entry("run-7")
		mutate(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: an incomplete entry was accepted", name)
		}
	}
}
