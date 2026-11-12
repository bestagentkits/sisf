// Package rbac decides, at the time of each action, whether an actor may run
// it. Roles come from two sources only: the actor's current GitHub
// permission on the target repository and roles granted locally in the
// factory. Roles an actor claims, issue labels, tool descriptions and model
// output are never read. The factory's app installation on a repository is
// checked separately from anyone's login: without an installation covering
// the target repository nothing is allowed there.
package rbac

import (
	"slices"
	"sync"

	"github.com/bestagentkits/sisf/pkg/core"
)

// humanRoles may be granted to people; the rest belong to agents and services.
var humanRoles = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleReporter, core.RoleViewer}

// RoleFitsKind reports whether role may be held by an actor of kind. People
// hold workspace roles; agents and services hold the agent, QA and publisher
// roles. Neither can be granted the other's.
func RoleFitsKind(role core.Role, kind core.ActorKind) bool {
	if !role.Valid() || !kind.Valid() {
		return false
	}
	return slices.Contains(humanRoles, role) == (kind == core.ActorHuman)
}

// Grants are the roles granted locally, by actor ID. They are kept in memory
// and seeded by the wiring from the factory's configuration; changing them
// at run time is an Owner action that passes through the action service.
type Grants struct {
	mu      sync.RWMutex
	byActor map[string]grantSet
}

type grantSet struct {
	kind  core.ActorKind
	roles []core.Role
}

// NewGrants returns an empty grant table.
func NewGrants() *Grants { return &Grants{byActor: map[string]grantSet{}} }

// Add grants role to the actor actorID of kind. An actor ID has one kind:
// granting it under another kind is refused.
func (g *Grants) Add(actorID string, kind core.ActorKind, role core.Role) error {
	switch {
	case actorID == "":
		return core.NewError(core.CodeInvalidArgument, "a grant needs an actor ID")
	case !RoleFitsKind(role, kind):
		return core.NewError(core.CodeInvalidArgument, "role "+string(role)+" cannot be granted to a "+string(kind)+" actor")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	set, ok := g.byActor[actorID]
	if ok && set.kind != kind {
		return core.NewError(core.CodeInvalidArgument, "actor "+actorID+" is already a "+string(set.kind)+" actor")
	}
	set.kind = kind
	if !slices.Contains(set.roles, role) {
		set.roles = append(slices.Clone(set.roles), role)
	}
	g.byActor[actorID] = set
	return nil
}

// Remove withdraws role from actorID; it takes effect at the next action.
func (g *Grants) Remove(actorID string, role core.Role) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set, ok := g.byActor[actorID]
	if !ok {
		return
	}
	set.roles = slices.DeleteFunc(slices.Clone(set.roles), func(r core.Role) bool { return r == role })
	if len(set.roles) == 0 {
		delete(g.byActor, actorID)
		return
	}
	g.byActor[actorID] = set
}

// Roles returns the roles granted to actorID as an actor of kind. Grants
// recorded under another kind do not apply.
func (g *Grants) Roles(actorID string, kind core.ActorKind) []core.Role {
	g.mu.RLock()
	defer g.mu.RUnlock()
	set, ok := g.byActor[actorID]
	if !ok || set.kind != kind {
		return nil
	}
	return slices.Clone(set.roles)
}
