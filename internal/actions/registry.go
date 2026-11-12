package actions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Registry is the one list of actions every surface serves. Domain packages
// expose their actions as []core.Action; the integration package registers
// them here, so the registry never imports a domain.
//
// A fallback is an action registered for a name whose real owner is not
// wired: it answers honestly that the capability is missing. A real
// registration of the same name replaces it, in whichever order the two
// arrive; two real registrations of one name are refused.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
}

type entry struct {
	action   core.Action
	fallback bool
	// subject is set for an action whose approval the performing component
	// spends; see RegisterDelegated.
	subject ApprovalSubject
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{entries: map[string]entry{}} }

// Register adds real actions. The batch is checked whole first: one invalid
// or duplicate action leaves the registry unchanged. A name already held by
// a real action is ALREADY_EXISTS; a name held by a fallback is replaced.
func (r *Registry) Register(actions ...core.Action) error {
	return r.add(actions, false, nil)
}

// RegisterFallback adds actions only for names nothing else holds. A name
// already registered keeps its action, so a fallback never displaces the
// real owner.
func (r *Registry) RegisterFallback(actions ...core.Action) error {
	return r.add(actions, true, nil)
}

func (r *Registry) add(actions []core.Action, fallback bool, subjects map[string]ApprovalSubject) error {
	seen := map[string]bool{}
	for _, a := range actions {
		if err := validateAction(a); err != nil {
			return err
		}
		if seen[a.Spec.Name] {
			return core.NewError(core.CodeAlreadyExists, fmt.Sprintf("action %s is listed twice", a.Spec.Name))
		}
		seen[a.Spec.Name] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !fallback {
		for _, a := range actions {
			if held, ok := r.entries[a.Spec.Name]; ok && !held.fallback {
				return core.NewError(core.CodeAlreadyExists, fmt.Sprintf("action %s is already registered", a.Spec.Name))
			}
		}
	}
	for _, a := range actions {
		if _, held := r.entries[a.Spec.Name]; held && fallback {
			continue
		}
		r.entries[a.Spec.Name] = entry{action: a, fallback: fallback, subject: subjects[a.Spec.Name]}
	}
	return nil
}

// validateAction applies core.ActionSpec.Validate plus what a served action
// needs: a handler, a summary for the generated documents, at least one
// token scope, and object schemas.
func validateAction(a core.Action) error {
	if err := a.Spec.Validate(); err != nil {
		return err
	}
	switch {
	case a.Handle == nil:
		return invalidf("action %s has no handler", a.Spec.Name)
	case strings.TrimSpace(a.Spec.Summary) == "":
		return invalidf("action %s needs a summary", a.Spec.Name)
	case len(a.Spec.Scopes) == 0:
		return invalidf("action %s must name the token scopes it needs", a.Spec.Name)
	}
	for _, scope := range a.Spec.Scopes {
		if strings.TrimSpace(scope) == "" {
			return invalidf("action %s names an empty scope", a.Spec.Name)
		}
	}
	for label, schema := range map[string]json.RawMessage{"input": a.Spec.Input, "output": a.Spec.Output} {
		if len(schema) == 0 {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(schema, &doc); err != nil || doc == nil {
			return invalidf("action %s has an %s schema that is not a JSON object", a.Spec.Name, label)
		}
	}
	return nil
}

// Lookup returns the action registered under name.
func (r *Registry) Lookup(name string) (core.Action, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	return e.action, ok
}

// subjectOf returns the approval subject of a delegated action, or nil.
func (r *Registry) subjectOf(name string) ApprovalSubject {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name].subject
}

// IsFallback reports whether name is served by a fallback.
func (r *Registry) IsFallback(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name].fallback
}

// Specs returns every registered spec, sorted by name.
func (r *Registry) Specs() []core.ActionSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ActionSpec, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.action.Spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// has reports whether name is registered.
func (r *Registry) has(name string) bool {
	_, ok := r.Lookup(name)
	return ok
}

// sameArea returns up to limit registered names that share name's area, the
// part before the first dot, so an unknown name can point at its neighbours.
func (r *Registry) sameArea(name string, limit int) []string {
	area, _, _ := strings.Cut(name, ".")
	var out []string
	for _, spec := range r.Specs() {
		if len(out) < limit && strings.HasPrefix(spec.Name, area+".") {
			out = append(out, spec.Name)
		}
	}
	return out
}

func invalidf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}
