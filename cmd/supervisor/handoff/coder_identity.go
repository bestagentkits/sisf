package handoff

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// CoderIdentity is the agent identity of the coding agent of a backend.
func CoderIdentity(backend string) string { return "agent:" + backend + "/coder" }

// backendName is the shape of a backend name in a dispatch record.
var backendName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// TrustedCoderIdentity names the coding agent of a run from the records the
// state store bound to the run, never from the patch document: the run is
// the one the store recorded the claim of for the issue, and the backend is
// the one the dispatcher started the agent attempt with, in the dispatch
// record it writes only under the issue's lease. It reports false when
// those records do not name one coder for exactly this run and agent
// attempt; the caller then marks the coder unverified.
func TrustedCoderIdentity(ctx context.Context, kv state.KV, record core.StateRecord, agentAttempt int) (string, bool, error) {
	entry, err := kv.GetKV(ctx, Namespace, "run/"+string(record.RunID))
	if core.HasCode(err, core.CodeNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var run dispatcher.RunRecord
	if json.Unmarshal(entry.Value, &run) != nil {
		return "", false, nil
	}
	switch {
	case record.RunID == "" || run.RunID != record.RunID || run.Issue != record.IssueID:
		return "", false, nil
	case run.Status != dispatcher.RunSucceeded || run.Attempt != agentAttempt || run.Agent == nil:
		return "", false, nil
	case run.Agent.RunID != record.RunID || !backendName.MatchString(run.Agent.Backend):
		return "", false, nil
	}
	return CoderIdentity(run.Agent.Backend), true, nil
}
