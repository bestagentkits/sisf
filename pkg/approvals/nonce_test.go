package approvals_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/approvals"
)

func TestNoncesAreLongEnoughAndDoNotRepeat(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 64 {
		nonce, err := approvals.NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		if len(nonce) < approvals.MinNonceLength || seen[nonce] {
			t.Fatalf("nonce %q is too short or repeats", nonce)
		}
		seen[nonce] = true
	}
}
