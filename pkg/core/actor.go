package core

import (
	"slices"
	"time"
)

// Role is a workspace role. Roles come from GitHub membership plus locally
// granted roles and are checked at the time of each action. An issue label,
// a tool description or a model's output never confers a role.
type Role string

const (
	// RoleOwner owns the factory deployment: the only role that widens
	// permissions, approves new features and changes policy.
	RoleOwner      Role = "owner"
	RoleMaintainer Role = "maintainer"
	RoleReporter   Role = "reporter"
	RoleViewer     Role = "viewer"
	// RoleAgent is a coding agent acting inside its delegated scope.
	RoleAgent Role = "agent"
	// RoleQA is the independent evaluator; never the same identity as the coder.
	RoleQA Role = "qa"
	// RolePublisher is the publisher or deployment service identity.
	RolePublisher Role = "publisher"
)

var allRoles = []Role{RoleOwner, RoleMaintainer, RoleReporter, RoleViewer, RoleAgent, RoleQA, RolePublisher}

// Roles returns the seven workspace roles. The caller owns the returned slice.
func Roles() []Role { return slices.Clone(allRoles) }

// Valid reports whether r is one of the seven roles.
func (r Role) Valid() bool { return slices.Contains(allRoles, r) }

// ActorKind says what is acting, so the audit trail can tell a person from
// an agent or a service.
type ActorKind string

const (
	ActorHuman   ActorKind = "human"
	ActorAgent   ActorKind = "agent"
	ActorService ActorKind = "service"
)

// Valid reports whether k is a known actor kind.
func (k ActorKind) Valid() bool {
	return k == ActorHuman || k == ActorAgent || k == ActorService
}

// Surface is a protocol through which an action arrives. Every surface calls
// the same action service; none defines permissions of its own.
type Surface string

const (
	SurfaceDashboard Surface = "dashboard"
	SurfaceAPI       Surface = "api"
	SurfaceCLI       Surface = "cli"
	SurfaceMCP       Surface = "mcp"
	SurfaceWebMCP    Surface = "webmcp"
)

var allSurfaces = []Surface{SurfaceDashboard, SurfaceAPI, SurfaceCLI, SurfaceMCP, SurfaceWebMCP}

// Surfaces returns the five control surfaces. The caller owns the returned slice.
func Surfaces() []Surface { return slices.Clone(allSurfaces) }

// Valid reports whether s is one of the five surfaces.
func (s Surface) Valid() bool { return slices.Contains(allSurfaces, s) }

// Actor is the authenticated party behind an action. It is built by the
// authentication layer from a verified session or token, never from request
// content, and is passed unchanged to authorization and audit.
type Actor struct {
	// ID is the internal actor identifier, stable across login providers.
	ID    string    `json:"id"`
	Kind  ActorKind `json:"kind"`
	Roles []Role    `json:"roles,omitempty"`
	// Scopes limit a token-bearing actor further than its roles do.
	Scopes  []string `json:"scopes,omitempty"`
	Surface Surface  `json:"surface,omitempty"`
	// SessionID ties the action to one session or token for revocation.
	SessionID string `json:"session_id,omitempty"`
	// StepUpAt is when a human last confirmed their identity for a sensitive
	// action. The zero value means never, so an ambient session alone cannot
	// satisfy a step-up requirement.
	StepUpAt time.Time `json:"step_up_at,omitzero"`
}

// HasRole reports whether the actor holds role.
func (a Actor) HasRole(role Role) bool { return slices.Contains(a.Roles, role) }
