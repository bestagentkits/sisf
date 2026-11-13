package stages

import (
	"context"
	"errors"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// runPolicyInForce checks that the policy in force is the one record's run
// was coded against, before anything of the run is built.
//
// A run is coded against the policy revision its approved scope was written
// under: the dispatcher pins every attempt's brief to it at claim, so the
// patch is a diff against that revision's tree. The supervisor builds the
// candidate on the policy in force and judges and releases it under that
// policy, so the two must be the same commit. When the Owner put another
// revision in force while the run was under way, the patch would be applied
// to a tree its coder never saw and judged by rules it was not written to.
// It is neither built nor judged on the new policy, and so never released
// on it: the build is held at once, spends no attempt, and the Owner is
// told on the issue. The run is over then: its reservation is closed, the
// control zone blocks the issue, and the Owner's retry has a new run code
// it under the policy in force.
//
// The revision is read from the scope intake approved, in the control
// zone, and the patch must name the same: the policy its run's brief was
// pinned to at claim, which a scope approved again during the run no
// longer is. What the agent zone wrote can only hold a build back here,
// never let one through.
func (s *BuildStage) runPolicyInForce(ctx context.Context, record core.StateRecord, patch handoff.PatchDoc, inForce core.PolicyRefs) error {
	var scope intake.SoW
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		scope, err = intake.ReadApprovedScope(ctx, tx, record.IssueID)
		return err
	})
	if err != nil {
		return err
	}
	if scope.PolicyRevision == inForce.PolicySHA && patch.Policy.PolicySHA == inForce.PolicySHA {
		return nil
	}
	superseded := core.NewError(core.CodePolicyHold, "the policy in force changed after the run was claimed")
	superseded.Details = map[string]string{"reason": "policy_superseded",
		"scope_policy": string(scope.PolicyRevision), "patch_policy": string(patch.Policy.PolicySHA),
		"policy_in_force": string(inForce.PolicySHA)}
	return superseded
}

// runBase returns the commit the run was coded on, which the candidate is
// built and judged on: the policy revision, or a merge the supervisor
// followed from it with the approved content unchanged, accepted again now.
// The patch's claim decides nothing on its own. A commit that is not
// accepted ends the run as a superseded policy does: the Owner's retry
// codes it again on the commit to build on then.
func (s *BuildStage) runBase(ctx context.Context, patch handoff.PatchDoc, inForce core.PolicyRefs) (core.CommitSHA, error) {
	base := patch.Base
	if base == "" {
		base = inForce.PolicySHA
	}
	accepted, err := acceptBase(ctx, s.Bases, inForce, base)
	if !core.HasCode(err, core.CodePolicyHold) {
		return accepted, err
	}
	superseded := core.NewError(core.CodePolicyHold, "the commit the run was coded on is not one the supervisor builds on now: "+messageOf(err))
	superseded.Details = map[string]string{"reason": "policy_superseded", "base": string(base), "policy_in_force": string(inForce.PolicySHA)}
	return "", superseded.WithCause(err)
}

// baseLacksLKG reports whether err is the refusal of a candidate whose base
// lacks its unit's last known good release.
func baseLacksLKG(err error) bool {
	var e *core.Error
	return errors.As(err, &e) && e.Code == core.CodeApprovalRequired && e.Details["reason"] == release.BaseLacksLKG
}

// messageOf is the message of the core error err carries, or err's text.
func messageOf(err error) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// policySuperseded reports whether err is runPolicyInForce's refusal.
func policySuperseded(err error) bool {
	var e *core.Error
	return errors.As(err, &e) && e.Code == core.CodePolicyHold && e.Details["reason"] == "policy_superseded"
}
