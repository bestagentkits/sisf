package oauthas

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// scopePattern matches the factory's scope names, such as "runs:read"; it
// is the same shape the authentication service accepts for tokens.
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*([.:][a-z][a-z0-9_]*)*$`)

const maxScopes = 64

// ScopePolicy decides which scopes a person may delegate to a client right
// now. A grant never exceeds it, and the authorizer still checks the
// person's roles at every action, so a scope is a ceiling, never a right.
type ScopePolicy interface {
	GrantableScopes(ctx context.Context, actor core.Actor) ([]string, error)
}

// ScopePolicyFunc adapts a function to ScopePolicy.
type ScopePolicyFunc func(ctx context.Context, actor core.Actor) ([]string, error)

// GrantableScopes calls f.
func (f ScopePolicyFunc) GrantableScopes(ctx context.Context, actor core.Actor) ([]string, error) {
	return f(ctx, actor)
}

// RoleScopes grants the scopes of every action that one of the actor's
// current roles may run. Roles resolves the roles at the time of the grant,
// for example with the authorizer; Specs lists the registered actions.
type RoleScopes struct {
	Roles func(ctx context.Context, actor core.Actor) ([]core.Role, error)
	Specs func() []core.ActionSpec
}

// GrantableScopes implements ScopePolicy.
func (p RoleScopes) GrantableScopes(ctx context.Context, actor core.Actor) ([]string, error) {
	if p.Roles == nil || p.Specs == nil {
		return nil, core.NewError(core.CodeInternal, "the scope policy is not configured")
	}
	roles, err := p.Roles(ctx, actor)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spec := range p.Specs() {
		if slices.ContainsFunc(spec.Roles, func(r core.Role) bool { return slices.Contains(roles, r) }) {
			out = append(out, spec.Scopes...)
		}
	}
	return normalizeScopes(out), nil
}

// ScopesOf returns every scope the specs name, sorted and distinct, for
// Config.ScopesSupported.
func ScopesOf(specs []core.ActionSpec) []string {
	var out []string
	for _, spec := range specs {
		out = append(out, spec.Scopes...)
	}
	return normalizeScopes(out)
}

// normalizeScopes sorts, de-duplicates and drops malformed names.
func normalizeScopes(scopes []string) []string {
	out := slices.DeleteFunc(slices.Clone(scopes), func(s string) bool { return !scopePattern.MatchString(s) })
	slices.Sort(out)
	return slices.Compact(out)
}

// parseScopeParam splits a space-delimited scope parameter. ok is false for
// a malformed or oversized list.
func parseScopeParam(raw string) (scopes []string, ok bool) {
	fields := strings.Fields(raw)
	if len(fields) > maxScopes {
		return nil, false
	}
	for _, f := range fields {
		if !scopePattern.MatchString(f) {
			return nil, false
		}
	}
	return normalizeScopes(fields), true
}

// grantScopes returns the scopes to grant for a request: all grantable ones
// when none is asked for, otherwise exactly those asked for, which must all
// be grantable. ok is false when the request asks for more than is held.
func grantScopes(requested, grantable []string) (granted []string, ok bool) {
	if len(requested) == 0 {
		return slices.Clone(grantable), len(grantable) > 0
	}
	for _, sc := range requested {
		if !slices.Contains(grantable, sc) {
			return nil, false
		}
	}
	return slices.Clone(requested), true
}
