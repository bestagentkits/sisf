package agentrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/redact"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// maxItems bounds each list of a handoff; the oldest entries go first.
	maxItems = 50
	// maxItemLen bounds one decision or evidence entry.
	maxItemLen = 2048
)

// extend returns the handoff with decisions and evidence added. Entries are
// redacted, trimmed, bounded and never repeated, so the summary stays small
// however many attempts a run takes and never carries a credential.
func extend(h agent.Handoff, decisions, evidence []string) agent.Handoff {
	h.Decisions = bounded(h.Decisions, decisions)
	h.Evidence = bounded(h.Evidence, evidence)
	return h
}

func bounded(have, add []string) []string {
	out := slices.Clone(have)
	for _, item := range add {
		item = clip(strings.TrimSpace(redact.Text(item)))
		if item == "" || slices.Contains(out, item) {
			continue
		}
		out = append(out, item)
	}
	if over := len(out) - maxItems; over > 0 {
		out = out[over:]
	}
	return out
}

// sanitize redacts the free text of a handoff supplied by a caller.
func sanitize(h agent.Handoff) agent.Handoff {
	h.SoW, h.DoD = redact.Text(h.SoW), redact.Text(h.DoD)
	return extend(agent.Handoff{SoW: h.SoW, DoD: h.DoD}, h.Decisions, h.Evidence)
}

// digest identifies a handoff by content, so a record shows exactly what an
// attempt started from.
func digest(h agent.Handoff) core.Digest {
	raw, _ := json.Marshal(h)
	sum := sha256.Sum256(raw)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}
