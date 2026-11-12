package docs_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// noRecordMark is how the state diagram writes "the issue has no record yet".
const noRecordMark = "[*]"

var (
	lifecycleHeadingRe = regexp.MustCompile(`^#{2,4} 5\.2(\s|$)`)
	anyHeadingRe       = regexp.MustCompile(`^#{1,6} `)
	// arrowRe reads "From --> To" with an optional ": label".
	arrowRe = regexp.MustCompile(`^(\[\*\]|[A-Za-z]+) --> (\[\*\]|[A-Za-z]+)(:\s*\S.*)?$`)
)

// arrow is one transition of the lifecycle, by the names the brief uses.
type arrow struct{ from, to string }

func (a arrow) String() string { return a.from + " --> " + a.to }

// lifecycleDiagram returns the arrows of the state diagram in section 5.2 of
// a brief. Every line of the diagram must be an arrow, so a diagram written
// in a form this reader does not know fails instead of being read as empty.
func lifecycleDiagram(lines []string) ([]arrow, error) {
	start := slices.IndexFunc(lines, lifecycleHeadingRe.MatchString)
	if start < 0 {
		return nil, fmt.Errorf("no section 5.2 heading")
	}
	section := lines[start+1:]
	if end := slices.IndexFunc(section, anyHeadingRe.MatchString); end >= 0 {
		section = section[:end]
	}
	open := slices.Index(section, "```mermaid")
	if open < 0 {
		return nil, fmt.Errorf("section 5.2 has no mermaid diagram")
	}
	body := section[open+1:]
	closed := slices.Index(body, "```")
	if closed < 0 {
		return nil, fmt.Errorf("the diagram of section 5.2 is not closed")
	}
	body = body[:closed]
	if len(body) == 0 || strings.TrimSpace(body[0]) != "stateDiagram-v2" {
		return nil, fmt.Errorf("the diagram of section 5.2 is not a stateDiagram-v2")
	}
	var arrows []arrow
	for _, line := range body[1:] {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		m := arrowRe.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("diagram line %q is not an arrow between two states", text)
		}
		arrows = append(arrows, arrow{from: m[1], to: m[2]})
	}
	if len(arrows) == 0 {
		return nil, fmt.Errorf("the diagram of section 5.2 has no arrows")
	}
	return arrows, nil
}

// tableArrows returns the transition table by the names the brief uses.
func tableArrows() []arrow {
	var arrows []arrow
	for _, e := range statemachine.Edges() {
		from := string(e.From)
		if from == "" {
			from = noRecordMark
		}
		arrows = append(arrows, arrow{from: from, to: string(e.To)})
	}
	return arrows
}

// disagreements lists every difference between the diagram of the brief and
// the transition table: an arrow only one of them has, an arrow they hold a
// different number of times, and a state only one of them knows.
func disagreements(diagram, table []arrow) []string {
	var out []string
	inDiagram, inTable := map[arrow]int{}, map[arrow]int{}
	for _, a := range diagram {
		inDiagram[a]++
	}
	for _, a := range table {
		inTable[a]++
	}
	for _, a := range diagram {
		if inTable[a] == 0 {
			out = append(out, fmt.Sprintf("the brief draws %s, the table has no such edge", a))
		}
	}
	for _, a := range table {
		switch {
		case inDiagram[a] == 0:
			out = append(out, fmt.Sprintf("the table has %s, the brief draws no such arrow", a))
		case inDiagram[a] != inTable[a]:
			out = append(out, fmt.Sprintf("the brief draws %s %d time(s), the table has it %d time(s)", a, inDiagram[a], inTable[a]))
		}
	}

	drawn := map[string]bool{}
	for _, a := range diagram {
		drawn[a.from], drawn[a.to] = true, true
	}
	delete(drawn, noRecordMark)
	for _, state := range core.States() {
		if !drawn[string(state)] {
			out = append(out, fmt.Sprintf("state %s is not in the diagram of the brief", state))
		}
		delete(drawn, string(state))
	}
	for name := range drawn {
		out = append(out, fmt.Sprintf("the brief draws state %s, which is not a lifecycle state", name))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestTheTransitionTableMatchesTheStateDiagramOfTheBrief(t *testing.T) {
	t.Parallel()
	for name, path := range map[string]string{"English brief": briefEN, "Vietnamese brief": briefVI} {
		diagram, err := lifecycleDiagram(readLines(t, path))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, difference := range disagreements(diagram, tableArrows()) {
			t.Errorf("%s: %s", name, difference)
		}
		if len(diagram) != len(statemachine.Edges()) {
			t.Errorf("%s: the diagram has %d arrows, the table has %d edges", name, len(diagram), len(statemachine.Edges()))
		}
	}
}

func TestAChangeToTheDiagramOfTheBriefIsReportedAgainstTheTable(t *testing.T) {
	t.Parallel()
	brief := strings.Join(readLines(t, briefEN), "\n")
	edits := map[string]struct {
		old, new string
		reports  string
	}{
		"a shortcut is drawn": {
			"  Intake --> Clarifying: missing information\n",
			"  Intake --> Clarifying: missing information\n  Intake --> Closed: nothing to do\n",
			"the brief draws Intake --> Closed, the table has no such edge",
		},
		"an arrow is removed": {
			"  Verified --> Closed: close issue + changelog + feature catalog\n", "",
			"the table has Verified --> Closed, the brief draws no such arrow",
		},
		"an arrow is redirected": {
			"  Canary --> Monitoring: rollout healthy\n", "  Canary --> Verified: rollout healthy\n",
			"the brief draws Canary --> Verified, the table has no such edge",
		},
		"an arrow is reversed": {
			"  RolledBack --> Blocked: evidence + recovery report\n", "  Blocked --> RolledBack: evidence + recovery report\n",
			"the brief draws Blocked --> RolledBack, the table has no such edge",
		},
		"an arrow is drawn twice": {
			"  Claimed --> Developing\n", "  Claimed --> Developing\n  Claimed --> Developing\n",
			"the brief draws Claimed --> Developing 2 time(s), the table has it 1 time(s)",
		},
		"a state is renamed": {
			"  ReleaseCandidate --> Canary\n", "  ReleaseCandidate --> Staging\n",
			"the brief draws state Staging, which is not a lifecycle state",
		},
		"the creation arrow is removed": {
			"  [*] --> Intake\n", "",
			"the table has [*] --> Intake, the brief draws no such arrow",
		},
	}
	for name, edit := range edits {
		changed := strings.Replace(brief, edit.old, edit.new, 1)
		if changed == brief {
			t.Errorf("%s: the brief no longer contains %q, so this case tests nothing", name, edit.old)
			continue
		}
		diagram, err := lifecycleDiagram(strings.Split(changed, "\n"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := disagreements(diagram, tableArrows()); !slices.Contains(got, edit.reports) {
			t.Errorf("%s: want %q among the differences, got %q", name, edit.reports, got)
		}
	}
}

func TestAChangeToTheTransitionTableIsReportedAgainstTheBrief(t *testing.T) {
	t.Parallel()
	diagram, err := lifecycleDiagram(readLines(t, briefEN))
	if err != nil {
		t.Fatal(err)
	}
	table := tableArrows()
	cases := map[string]struct {
		table   []arrow
		reports string
	}{
		"the table gains a shortcut": {
			append(slices.Clone(table), arrow{"Intake", "Closed"}),
			"the table has Intake --> Closed, the brief draws no such arrow",
		},
		"the table loses an edge": {
			slices.DeleteFunc(slices.Clone(table), func(a arrow) bool { return a == arrow{"Monitoring", "RolledBack"} }),
			"the brief draws Monitoring --> RolledBack, the table has no such edge",
		},
		"the table holds an edge twice": {
			append(slices.Clone(table), arrow{"Ready", "Claimed"}),
			"the brief draws Ready --> Claimed 1 time(s), the table has it 2 time(s)",
		},
	}
	for name, tc := range cases {
		if got := disagreements(diagram, tc.table); !slices.Contains(got, tc.reports) {
			t.Errorf("%s: want %q among the differences, got %q", name, tc.reports, got)
		}
	}
}

func TestADiagramThatCannotBeReadFailsInsteadOfPassingEmpty(t *testing.T) {
	t.Parallel()
	const fence = "```"
	cases := map[string]string{
		"no section 5.2":            "## 5. Workflow\n\n### 5.1 Inputs\n",
		"section without a diagram": "### 5.2 State machine\n\nprose only\n\n### 5.3 Next\n",
		"diagram in a later section": "### 5.2 State machine\n\n### 5.3 Next\n\n" +
			fence + "mermaid\nstateDiagram-v2\n  [*] --> Intake\n" + fence + "\n",
		"diagram never closed":   "### 5.2 State machine\n\n" + fence + "mermaid\nstateDiagram-v2\n  [*] --> Intake\n",
		"not a state diagram":    "### 5.2 State machine\n\n" + fence + "mermaid\nflowchart LR\n  Intake --> Closed\n" + fence + "\n",
		"diagram without arrows": "### 5.2 State machine\n\n" + fence + "mermaid\nstateDiagram-v2\n" + fence + "\n",
		"a line that is not an arrow": "### 5.2 State machine\n\n" + fence + "mermaid\nstateDiagram-v2\n" +
			"  [*] --> Intake\n  state Intake {\n" + fence + "\n",
		"an arrow written another way": "### 5.2 State machine\n\n" + fence + "mermaid\nstateDiagram-v2\n" +
			"  [*] --> Intake\n  Intake -> Closed\n" + fence + "\n",
	}
	for name, text := range cases {
		if arrows, err := lifecycleDiagram(strings.Split(text, "\n")); err == nil {
			t.Errorf("%s: read %d arrows without an error", name, len(arrows))
		}
	}
}
