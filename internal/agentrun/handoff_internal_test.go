package agentrun

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent"
)

func TestAHandoffStaysBoundedAndNeverRepeatsAnEntry(t *testing.T) {
	t.Parallel()
	h := agent.Handoff{SoW: "scope", DoD: "done"}
	for i := 0; i < 3*maxItems; i++ {
		h = extend(h, []string{fmt.Sprintf("decision %d", i), "decision 0"}, []string{strings.Repeat("e", 2*maxItemLen)})
	}
	if len(h.Decisions) != maxItems || len(h.Evidence) != 1 {
		t.Fatalf("decisions %d evidence %d, want %d and 1", len(h.Decisions), len(h.Evidence), maxItems)
	}
	if h.Decisions[len(h.Decisions)-1] != fmt.Sprintf("decision %d", 3*maxItems-1) {
		t.Errorf("the newest decision must be kept, got %q", h.Decisions[len(h.Decisions)-1])
	}
	if len(h.Evidence[0]) > maxItemLen+3 {
		t.Errorf("one entry is %d bytes", len(h.Evidence[0]))
	}
	if h.SoW != "scope" || h.DoD != "done" {
		t.Error("the scope and the definition of done are never trimmed")
	}
}

func TestADigestNamesTheContentOfAHandoff(t *testing.T) {
	t.Parallel()
	a := agent.Handoff{SoW: "x", DoD: "y"}
	b := extend(a, []string{"a decision"}, nil)
	if digest(a) == digest(b) || digest(a) != digest(agent.Handoff{SoW: "x", DoD: "y"}) {
		t.Error("equal handoffs share a digest and different ones do not")
	}
	if err := digest(a).Validate(); err != nil {
		t.Error(err)
	}
}
