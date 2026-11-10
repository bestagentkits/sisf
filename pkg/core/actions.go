package core

import (
	"context"
	"encoding/json"
	"regexp"
)

// Sensitivity classifies what an action can change. It decides how much
// ceremony surrounds the action, and a surface cannot lower it.
type Sensitivity string

const (
	// SensitivityRead changes nothing.
	SensitivityRead Sensitivity = "read"
	// SensitivityWrite changes state inside the actor's existing authority.
	SensitivityWrite Sensitivity = "write"
	// SensitivitySensitive changes permissions, policy, money, secrets or
	// releases and always goes through plan, approval, execute, audit.
	SensitivitySensitive Sensitivity = "sensitive"
)

// Valid reports whether s is a known sensitivity.
func (s Sensitivity) Valid() bool {
	return s == SensitivityRead || s == SensitivityWrite || s == SensitivitySensitive
}

// NeedsApproval reports whether actions of this sensitivity must pass the
// approval pipeline before they execute.
func (s Sensitivity) NeedsApproval() bool { return s == SensitivitySensitive }

// ActionSpec describes one action of the factory. All surfaces are generated
// from the same specs, so authorization and behavior cannot drift between
// them. The description is documentation only: it grants nothing.
type ActionSpec struct {
	// Name is the dotted action name, for example "approvals.decide".
	Name        string      `json:"name"`
	Summary     string      `json:"summary"`
	Sensitivity Sensitivity `json:"sensitivity"`
	// Scopes are the token scopes a token-bearing actor needs.
	Scopes []string `json:"scopes,omitempty"`
	// Roles lists the roles that may run the action at all.
	Roles []Role `json:"roles"`
	// StepUp demands a fresh, verified human confirmation.
	StepUp bool `json:"step_up,omitempty"`
	// Input and Output are JSON Schema documents for the payloads.
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

var actionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Mutates reports whether the action can change anything. Every mutating
// action supports a dry run.
func (s ActionSpec) Mutates() bool { return s.Sensitivity != SensitivityRead }

// Validate rejects specs that would register an ambiguous or unguarded action.
func (s ActionSpec) Validate() error {
	switch {
	case !actionNamePattern.MatchString(s.Name):
		return invalid("action name %q must be dotted lowercase such as area.verb", s.Name)
	case !s.Sensitivity.Valid():
		return invalid("action %s has unknown sensitivity %q", s.Name, string(s.Sensitivity))
	case len(s.Roles) == 0:
		return invalid("action %s must name the roles allowed to run it", s.Name)
	case s.StepUp && !s.Sensitivity.NeedsApproval():
		return invalid("action %s asks for step-up but is not marked sensitive", s.Name)
	}
	for _, role := range s.Roles {
		if !role.Valid() {
			return invalid("action %s names unknown role %q", s.Name, string(role))
		}
	}
	return nil
}

// ChangeOp is the kind of change a dry run reports.
type ChangeOp string

const (
	ChangeCreate ChangeOp = "create"
	ChangeUpdate ChangeOp = "update"
	ChangeDelete ChangeOp = "delete"
)

// Valid reports whether op is a known change operation.
func (op ChangeOp) Valid() bool {
	return op == ChangeCreate || op == ChangeUpdate || op == ChangeDelete
}

// Change is one effect a write would have.
type Change struct {
	Resource string          `json:"resource"`
	Op       ChangeOp        `json:"op"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
}

// DryRun is the answer to a write asked with dry_run: exactly what would
// change, with nothing changed. A dry run never substitutes for approval.
type DryRun struct {
	Changes          []Change `json:"changes"`
	RequiresApproval bool     `json:"requires_approval"`
	// Warnings carry risk notices, for example when a policy would be widened.
	Warnings []string `json:"warnings,omitempty"`
}

// ActionRequest is one invocation of an action, identical on every surface.
type ActionRequest struct {
	Actor  Actor           `json:"actor"`
	Input  json.RawMessage `json:"input,omitempty"`
	DryRun bool            `json:"dry_run,omitempty"`
	// IdempotencyKey makes a retried write apply once.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// ApprovalID names the approval that authorizes a sensitive write.
	ApprovalID string `json:"approval_id,omitempty"`
}

// ActionResult is the outcome of an invocation. Plan is set for a dry run.
type ActionResult struct {
	Output  json.RawMessage `json:"output,omitempty"`
	Plan    *DryRun         `json:"plan,omitempty"`
	AuditID string          `json:"audit_id,omitempty"`
}

// ActionHandler performs an action. It runs only after authorization and
// returns a typed *Error on failure.
type ActionHandler func(ctx context.Context, req ActionRequest) (ActionResult, error)

// Action pairs a spec with its handler. Domain packages expose their actions
// as a list of these; only the action service invokes the handler.
type Action struct {
	Spec   ActionSpec
	Handle ActionHandler
}

// AuthzEffect is the outcome of an authorization check.
type AuthzEffect string

const (
	EffectDeny  AuthzEffect = "deny"
	EffectAllow AuthzEffect = "allow"
	// EffectRequireApproval means the actor may ask but an approval must exist first.
	EffectRequireApproval AuthzEffect = "require_approval"
	// EffectRequireStepUp means the actor must confirm their identity first.
	EffectRequireStepUp AuthzEffect = "require_step_up"
)

// AuthzRequest asks whether an actor may run an action on a resource now.
type AuthzRequest struct {
	Actor  Actor      `json:"actor"`
	Action ActionSpec `json:"action"`
	// Resource names the target, for example an issue ref or a secret ref.
	Resource string `json:"resource,omitempty"`
}

// AuthzDecision is the answer. Its zero value denies, so a decision that was
// never filled in can never let an action through.
type AuthzDecision struct {
	Effect AuthzEffect `json:"effect"`
	Reason string      `json:"reason,omitempty"`
}

// Allowed reports whether the action may proceed right now.
func (d AuthzDecision) Allowed() bool { return d.Effect == EffectAllow }

// Authorizer is the single authorization point for every surface. It decides
// at the time of the action from the actor's current roles and grants; an
// error means the decision could not be made and must be treated as a denial.
type Authorizer interface {
	Authorize(ctx context.Context, req AuthzRequest) (AuthzDecision, error)
}
