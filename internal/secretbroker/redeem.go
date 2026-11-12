package secretbroker

import (
	"context"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// Use is a redemption that states who redeems and what for. The broker
// releases the value only when the grant was issued to that run for that
// purpose and, when a scope is named, within that scope.
type Use struct {
	GrantID string
	RunID   core.RunID
	Purpose string
	Scope   string
}

// Grant returns a live grant's description, without its value: NOT_FOUND
// for an unknown grant and EXPIRED for one that ran out or was revoked.
func (b *Broker) Grant(_ context.Context, grantID string) (secrets.Grant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, err := b.live(grantID)
	if err != nil {
		return secrets.Grant{}, err
	}
	out := g.Grant
	out.Scopes = slices.Clone(out.Scopes)
	return out, nil
}

// live returns the grant if it may still be redeemed. The caller holds b.mu.
func (b *Broker) live(grantID string) (*grant, error) {
	g, ok := b.grants[grantID]
	switch {
	case !ok:
		return nil, core.NewError(core.CodeNotFound, "no such grant")
	case g.revoked || g.Expired(b.cfg.Clock.Now()):
		return nil, core.NewError(core.CodeExpired, "the grant has expired or was revoked")
	}
	return g, nil
}

// Redeem returns the value of a live grant. It is the contract's form and
// checks nothing about the caller; consumers that know the run and the
// purpose they redeem for use RedeemFor.
func (b *Broker) Redeem(ctx context.Context, grantID string) (core.SecretValue, error) {
	b.mu.Lock()
	g, err := b.live(grantID)
	var ref secrets.SecretRef
	var run core.RunID
	if err == nil {
		ref, run = g.Ref, g.RunID
	}
	b.mu.Unlock()
	if err != nil {
		b.audit(ctx, "secrets.grant.redeem", "", secrets.SecretRef{}, err)
		return core.SecretValue{}, err
	}
	value, err := b.cfg.Provider.Resolve(ctx, ref)
	if err != nil {
		err = core.NewError(core.CodeOf(err), "the secret of the grant could not be resolved").WithCause(err)
	}
	b.audit(ctx, "secrets.grant.redeem", run, ref, err)
	return value, err
}

// RedeemFor returns the value of a live grant to the run it was issued to,
// for the purpose it was issued for and, when use names a scope, within
// the grant's scopes. Anything else is PERMISSION_DENIED.
func (b *Broker) RedeemFor(ctx context.Context, use Use) (core.SecretValue, error) {
	b.mu.Lock()
	g, err := b.live(use.GrantID)
	if err == nil {
		switch {
		case g.RunID != use.RunID:
			err = denied("the grant belongs to another run")
		case g.Purpose != use.Purpose:
			err = denied("the grant was issued for another purpose")
		case use.Scope != "" && !slices.Contains(g.Scopes, use.Scope):
			err = denied("the grant does not cover this scope")
		}
	}
	b.mu.Unlock()
	if err != nil {
		b.audit(ctx, "secrets.grant.redeem", use.RunID, secrets.SecretRef{}, err)
		return core.SecretValue{}, err
	}
	return b.Redeem(ctx, use.GrantID)
}

// RevokeRun ends every grant of the run at once and refuses the run any
// further grant: on cancel, on completion and on emergency stop.
func (b *Broker) RevokeRun(ctx context.Context, run core.RunID) error {
	if err := run.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	b.revoked[run] = true
	for _, g := range b.grants {
		if g.RunID == run {
			g.revoked = true
		}
	}
	b.mu.Unlock()
	b.audit(ctx, "secrets.run.revoke", run, secrets.SecretRef{}, nil)
	return nil
}

// audit records an action on references only. A failure to audit does not
// change the outcome of the action: the broker refuses by default and its
// refusals do not depend on the log.
func (b *Broker) audit(ctx context.Context, action string, run core.RunID, ref secrets.SecretRef, failure error) {
	if b.cfg.Audit == nil {
		return
	}
	outcome := core.OutcomeExecuted
	details := map[string]string{}
	if failure != nil {
		outcome = core.OutcomeDenied
		details["code"] = string(core.CodeOf(failure))
	}
	if ref.Name != "" {
		details["secret"] = ref.String()
	}
	event := core.AuditEvent{FactoryID: b.cfg.FactoryID, ActorID: "service:secret-broker", ActorKind: core.ActorService,
		Action: action, Resource: "secrets", Outcome: outcome, Details: details}
	if run.Validate() == nil {
		event.RunID = run
	}
	_, _ = b.cfg.Audit.Append(ctx, event) // see the comment above
}
