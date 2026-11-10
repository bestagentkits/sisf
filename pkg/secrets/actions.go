package secrets

import (
	"context"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ActionListRefs is the name of the action that lists secret references.
const ActionListRefs = "secrets.list_refs"

const (
	listRefsInput  = `{"type":"object","additionalProperties":false}`
	listRefsOutput = `{"type":"object","required":["refs"],"properties":{"refs":{"type":"array","items":{"type":"object",` +
		`"required":["provider","name","class","zone"],"properties":{"provider":{"type":"string"},"name":{"type":"string"},` +
		`"class":{"type":"string"},"zone":{"type":"string"}}}}}}`
)

// RefList is the output of secrets.list_refs. It is built from SecretRef
// only, a type that has no field for a value, so a value cannot reach it.
type RefList struct {
	Refs []SecretRef `json:"refs"`
}

// Actions returns the secret-reference actions of the factory. They read
// references from the given providers and never resolve a secret: the
// dashboard, API, CLI and MCP can show which secrets exist, not what they hold.
func Actions(providers ...SecretProvider) []core.Action {
	spec := core.ActionSpec{
		Name:        ActionListRefs,
		Summary:     "List secret references by provider, name, class and zone; never values.",
		Sensitivity: core.SensitivityRead,
		Scopes:      []string{"secrets:read"},
		Roles:       []core.Role{core.RoleOwner, core.RoleMaintainer},
		Input:       actions.Schema(listRefsInput),
		Output:      actions.Schema(listRefsOutput),
	}
	return []core.Action{{Spec: spec, Handle: listRefs(providers)}}
}

func listRefs(providers []SecretProvider) core.ActionHandler {
	return func(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
		if _, err := actions.DecodeInput[struct{}](req); err != nil {
			return core.ActionResult{}, err
		}
		list := RefList{Refs: []SecretRef{}}
		for _, p := range providers {
			if p == nil {
				continue
			}
			refs, err := p.ListRefs(ctx)
			if err != nil {
				return core.ActionResult{}, core.NewError(core.CodeUnavailable, "a secret provider could not list its references").WithCause(err)
			}
			for _, ref := range refs {
				// Copy the four reference fields only, so nothing a provider
				// attaches to a richer type can be serialized.
				list.Refs = append(list.Refs, SecretRef{Provider: ref.Provider, Name: ref.Name, Class: ref.Class, Zone: ref.Zone})
			}
		}
		slices.SortFunc(list.Refs, func(a, b SecretRef) int { return strings.Compare(a.String(), b.String()) })
		return actions.JSONResult(list)
	}
}
