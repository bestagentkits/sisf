package conformance_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance/conformancetest"
	"github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
)

// What a backend, or whoever grades it, writes down is a claim. Only the
// kit's own run is evidence, and nothing but a kit run yields "supported".
func TestSupportCannotBeSelfAttested(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	declared := map[conformance.Kind]core.CapabilitySet{
		conformance.KindExecution: conformancetest.NewExecution("e").Declared,
		conformance.KindDeploy:    conformancetest.NewDeploy("d").Declared,
		conformance.KindAgent:     conformancetest.NewAgent("a").Declared,
	}
	for kind, capabilities := range declared {
		// Every area declared verified, and still no more than a claim.
		row := conformance.Grade(conformance.Declaration{Kind: kind, Name: "self-attested", Capabilities: capabilities})
		if row.Verdict != conformance.VerdictExperimental {
			t.Errorf("%s: a declaration alone is graded %q, want experimental", kind, row.Verdict)
		}
		// A matrix built from declarations holds claims only.
		if matrix := conformance.NewMatrix(conformance.Declaration{Kind: kind, Name: "self-attested", Capabilities: capabilities}); matrix[0].Verdict != conformance.VerdictExperimental {
			t.Errorf("%s: a matrix row built from a declaration is graded %q, want experimental", kind, matrix[0].Verdict)
		}
	}

	// A declaration written as JSON cannot carry the proof either.
	var forged conformance.Declaration
	text := `{"kind":"execution","name":"forged","end_to_end_proven":true,"exercised":true,"capabilities":{
		"lifecycle":"supported","rollback":"supported","observability":"supported",
		"execution.isolation":"supported","execution.egress_enforcement":"supported"}}`
	if err := json.Unmarshal([]byte(text), &forged); err != nil {
		t.Fatal(err)
	}
	if row := conformance.Grade(forged); row.Verdict != conformance.VerdictExperimental {
		t.Errorf("a declaration read from JSON is graded %q, want experimental", row.Verdict)
	}

	// A kit run the backend fails partway never reaches the proof.
	broken := conformancetest.NewExecution("broken")
	broken.Fail["Destroy"] = errBroken
	if row, err := conformance.GradeExecution(ctx, broken, executionProbe()); err != nil || row.Verdict == conformance.VerdictSupported {
		t.Errorf("a failed run is graded %q, err %v; want it below supported", row.Verdict, err)
	}
}
