package processstart

import (
	"context"
	"os"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A ps that is cut off said nothing about the process: it is not taken for
// gone, which would let a caller believe a live process had stopped.
func TestAPSThatIsCutOffDoesNotSayTheProcessIsGone(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if start, err := psStartWithin(ctx, os.Getpid()); start != "" || !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("psStartWithin = %q, %v; want UNAVAILABLE", start, err)
	}
}
