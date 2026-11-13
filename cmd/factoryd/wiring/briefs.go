package wiring

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the control zone publishes the references of each approved policy
// revision for the agent zone. The policy is read and judged in the control
// zone; the agent zone reads only what was published, so it needs neither
// GitHub nor a GitHub credential to start a run.
const (
	policyRefsNamespace = "control.policy"
	policyRefsPrefix    = "refs/"
)

// PublishPolicyRefs stores the references a coding run under snapshot is
// pinned to. They are written once per revision: a commit's content never
// changes, so neither do they.
func PublishPolicyRefs(ctx context.Context, store state.Store, snapshot *policy.Snapshot) error {
	refs, err := snapshot.Refs(core.StepCoding)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return core.NewError(core.CodeInternal, "the policy references cannot be encoded").WithCause(err)
	}
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.GetKV(ctx, policyRefsNamespace, policyRefsPrefix+string(snapshot.Revision))
		switch {
		case err == nil:
			return nil
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: policyRefsNamespace, Key: policyRefsPrefix + string(snapshot.Revision), Value: raw})
		return err
	})
}

// readPolicyRefs reads the published references of revision through kv.
func readPolicyRefs(ctx context.Context, kv state.KV, revision core.CommitSHA) (core.PolicyRefs, error) {
	entry, err := kv.GetKV(ctx, policyRefsNamespace, policyRefsPrefix+string(revision))
	if core.HasCode(err, core.CodeNotFound) {
		return core.PolicyRefs{}, core.NewError(core.CodePolicyHold,
			"the policy the scope was written under has not been published to the agent zone yet")
	}
	if err != nil {
		return core.PolicyRefs{}, err
	}
	var refs core.PolicyRefs
	if err := json.Unmarshal(entry.Value, &refs); err != nil {
		return core.PolicyRefs{}, core.NewError(core.CodeTampered, "the published policy references cannot be read")
	}
	if refs.PolicySHA != revision {
		return core.PolicyRefs{}, core.NewError(core.CodeTampered, "the published policy references name another revision")
	}
	return refs, refs.Validate()
}

// StoreBriefs is the brief source of the dispatcher: the scope an issue
// became Ready with, exactly as intake stored it, pinned to the policy
// revision that scope was written under. Both are read in one transaction,
// so a run never pairs a scope with another policy.
func StoreBriefs(store state.Store) dispatcher.BriefSource {
	return dispatcher.BriefFunc(func(ctx context.Context, issue core.IssueRef) (dispatcher.Brief, error) {
		var brief dispatcher.Brief
		err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			scope, err := intake.ReadApprovedScope(ctx, tx, issue)
			if err != nil {
				return err
			}
			refs, err := readPolicyRefs(ctx, tx, scope.PolicyRevision)
			if err != nil {
				return err
			}
			base, _, err := policy.ReadBuildBase(ctx, tx)
			if err != nil {
				return err
			}
			if stop := base.StopFor(scope.PolicyRevision); stop != nil {
				// Nothing is coded that could not be released: the
				// supervisor did not follow a merge, so a run on this policy
				// would be built on a commit without that release.
				return core.NewError(core.CodeApprovalRequired, "the factory stopped building on main at "+string(stop.Merge)+
					" ("+stop.Reason+"); no run is started until the Owner approves a policy revision that contains it")
			}
			brief = BriefOf(scope, refs)
			if commit := base.BaseFor(scope.PolicyRevision); commit != scope.PolicyRevision {
				brief.Base = commit
			}
			return nil
		})
		return brief, err
	})
}

// BriefOf turns an approved scope into what an agent starts from. The scope
// text and the criteria are data the issue supplied; the agent receives them
// as its task, never as rules about its permissions.
func BriefOf(scope intake.SoW, refs core.PolicyRefs) dispatcher.Brief {
	var dod strings.Builder
	for i, item := range scope.DoD {
		if i > 0 {
			dod.WriteString("\n")
		}
		dod.WriteString("- " + item)
	}
	return dispatcher.Brief{
		Handoff:     agent.Handoff{SoW: scope.Text(), DoD: dod.String(), Evidence: []string{"issue:" + scope.Issue.String()}},
		Policy:      refs,
		ScopeDigest: scope.Digest,
	}
}
