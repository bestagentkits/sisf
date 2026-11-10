package cliagent

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// FinalReport is what a finished run left as its result.
type FinalReport struct {
	// Result is the tool's explicit final result, redacted, when HasResult
	// is set; tools without such a marker leave it empty.
	Result    string
	HasResult bool
	// Complete is false when events past the cap were dropped or an output
	// line over the size limit was skipped: the record of the run is then
	// not the whole of what the tool said, and its last message may not be
	// its last word.
	Complete bool
}

// FinalReport returns what a finished run reported. It is CONFLICT while
// the run is still going.
func (b *Backend) FinalReport(_ context.Context, ref agent.RunRef) (FinalReport, error) {
	r, err := b.find(ref)
	if err != nil {
		return FinalReport{}, err
	}
	select {
	case <-r.done:
	default:
		return FinalReport{}, core.NewError(core.CodeConflict, "the run has not finished yet")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	report := FinalReport{Complete: !r.dropped && !r.truncated}
	if r.final != nil && r.final.HasResult {
		report.Result, report.HasResult = r.b.redactor.Text(r.final.Result), true
	}
	return report, nil
}
