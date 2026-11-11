package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// heldBuildMarker identifies the notice of one hold of an attempt's build,
// or one reminder of it.
func heldBuildMarker(n handoff.BuildHeldNotice) string {
	id := string(n.RunID) + "/" + strconv.Itoa(n.Attempt) + "/" + strconv.Itoa(n.Holds)
	if n.Reminder > 0 {
		id += "/reminder-" + strconv.Itoa(n.Reminder)
	}
	return "<!-- sisf:build-held " + id + " -->"
}

// deliverBuildHeld posts the supervisor's notice that a candidate build is
// held on the issue, once per hold.
func (c *Control) deliverBuildHeld(ctx context.Context, m state.OutboxMessage) error {
	var n handoff.BuildHeldNotice
	if err := json.Unmarshal(m.Payload, &n); err != nil || n.IssueID.Validate() != nil || n.RunID.Validate() != nil || n.Attempt < 1 {
		return core.NewError(core.CodeInvalidArgument, "the held build notice cannot be read")
	}
	comments, err := c.cfg.GitHub.ListComments(ctx, n.IssueID, time.Time{})
	if err != nil {
		return err
	}
	marker := heldBuildMarker(n)
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	_, err = c.cfg.GitHub.CreateComment(ctx, n.IssueID, heldBuildComment(n)+"\n\n"+marker)
	return err
}

// heldBuildComment is the notice as the Owner reads it. It names the error
// code only; no message of the failure is posted.
func heldBuildComment(n handoff.BuildHeldNotice) string {
	var b strings.Builder
	if n.Reminder > 0 {
		fmt.Fprintf(&b, "### Candidate build still held\n\nThe build of attempt %d of run `%s` has been held since %s "+
			"and waits for the Owner (reminder %d).\n\n", n.Attempt, n.RunID, n.HeldAt.UTC().Format("2006-01-02 15:04 MST"), n.Reminder)
	} else {
		fmt.Fprintf(&b, "### Candidate build held\n\nThe build of attempt %d of run `%s` failed %d times in a row on errors outside the patch "+
			"(GitHub, Docker or the configuration), so the supervisor stopped trying it. No attempt was spent.\n\n", n.Attempt, n.RunID, n.Failures)
	}
	fmt.Fprintf(&b, "- Last error: `%s`\n", oneLine(string(n.LastCode)))
	fmt.Fprintf(&b, "- To resume once the cause is fixed: `factoryd issue retry --issue %s`, as the Owner.", n.IssueID)
	return b.String()
}
