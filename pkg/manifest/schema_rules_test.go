package manifest

import "testing"

// A rule keyed by a field that was renamed or removed would silently stop
// constraining anything.
func TestEverySchemaRuleConstrainsARealField(t *testing.T) {
	if stale := staleSchemaRules(); len(stale) != 0 {
		t.Fatalf("schema rules that match no manifest type or key: %v", stale)
	}
}
