package core

import (
	"errors"
	"fmt"
	"slices"
)

// ErrorCode is the machine-readable class of a failure. Callers, including
// agents, branch on the code and never on message text.
type ErrorCode string

const (
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeAlreadyExists   ErrorCode = "ALREADY_EXISTS"
	// CodeUnauthenticated means the caller's identity could not be established.
	CodeUnauthenticated ErrorCode = "UNAUTHENTICATED"
	// CodePermissionDenied means the identified actor may not do this.
	CodePermissionDenied ErrorCode = "PERMISSION_DENIED"
	// CodeApprovalRequired means the action waits for an authorized approval.
	CodeApprovalRequired ErrorCode = "APPROVAL_REQUIRED"
	// CodeStepUpRequired means a fresh, verified human confirmation is needed.
	CodeStepUpRequired ErrorCode = "STEP_UP_REQUIRED"
	// CodeConflict means a compare-and-swap lost: the version read is stale.
	CodeConflict ErrorCode = "CONFLICT"
	// CodeInvalidTransition means the lifecycle has no such edge.
	CodeInvalidTransition ErrorCode = "INVALID_TRANSITION"
	// CodeGuardFailed means the edge exists but one of its guards does not hold.
	CodeGuardFailed ErrorCode = "GUARD_FAILED"
	// CodeLeaseHeld means another owner holds an unexpired lease.
	CodeLeaseHeld ErrorCode = "LEASE_HELD"
	// CodeStaleFencingToken means the writer lost its lease to a newer holder.
	CodeStaleFencingToken ErrorCode = "STALE_FENCING_TOKEN"
	// CodeReplayed means a single-use approval or nonce was presented again.
	CodeReplayed ErrorCode = "REPLAYED"
	CodeExpired  ErrorCode = "EXPIRED"
	// CodeSignatureInvalid means a signature, or the key behind it, did not verify.
	CodeSignatureInvalid ErrorCode = "SIGNATURE_INVALID"
	// CodeTampered means an append-only record was edited or removed.
	CodeTampered ErrorCode = "TAMPERED"
	// CodePolicyHold means policy sources conflict or are not at an approved commit.
	CodePolicyHold ErrorCode = "POLICY_HOLD"
	// CodeBudgetUnconfirmed means the Owner has not yet confirmed the budget mode.
	CodeBudgetUnconfirmed ErrorCode = "BUDGET_UNCONFIRMED"
	// CodeBudgetExceeded means a configured spending cap would be crossed.
	CodeBudgetExceeded ErrorCode = "BUDGET_EXCEEDED"
	// CodeLimitExceeded means a safety limit (time, attempts, concurrency,
	// quota) was hit. These limits apply even when the budget is unlimited.
	CodeLimitExceeded ErrorCode = "LIMIT_EXCEEDED"
	CodeRateLimited   ErrorCode = "RATE_LIMITED"
	// CodeNotSupported means a backend lacks the capability; it is an honest
	// refusal, never a silent no-op.
	CodeNotSupported ErrorCode = "NOT_SUPPORTED"
	// CodeReleaseHold means a release cannot be observed or rolled back
	// safely, so it waits instead of assuming success.
	CodeReleaseHold ErrorCode = "RELEASE_HOLD"
	// CodeFrozen means the emergency stop is active.
	CodeFrozen      ErrorCode = "FROZEN"
	CodeUnavailable ErrorCode = "UNAVAILABLE"
	CodeTimeout     ErrorCode = "TIMEOUT"
	// CodeInternal is also the code reported for any error without a code, so
	// an untyped failure is never mistaken for success.
	CodeInternal ErrorCode = "INTERNAL"
)

var allErrorCodes = []ErrorCode{
	CodeInvalidArgument, CodeNotFound, CodeAlreadyExists, CodeUnauthenticated,
	CodePermissionDenied, CodeApprovalRequired, CodeStepUpRequired, CodeConflict,
	CodeInvalidTransition, CodeGuardFailed, CodeLeaseHeld, CodeStaleFencingToken,
	CodeReplayed, CodeExpired, CodeSignatureInvalid, CodeTampered, CodePolicyHold,
	CodeBudgetUnconfirmed, CodeBudgetExceeded, CodeLimitExceeded, CodeRateLimited,
	CodeNotSupported, CodeReleaseHold, CodeFrozen, CodeUnavailable, CodeTimeout,
	CodeInternal,
}

// ErrorCodes returns every defined code. The caller owns the returned slice.
func ErrorCodes() []ErrorCode { return slices.Clone(allErrorCodes) }

// Valid reports whether c is a defined code.
func (c ErrorCode) Valid() bool { return slices.Contains(allErrorCodes, c) }

// NextAction is one valid step a caller may take after a failure, so an
// agent can continue without guessing.
type NextAction struct {
	// Action is the name of an action in the action registry.
	Action      string            `json:"action"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
}

// Error is the typed error every action returns. The message is safe to show
// to the caller; the cause is kept for operators and never serialized.
type Error struct {
	Code        ErrorCode         `json:"code"`
	Message     string            `json:"message"`
	NextActions []NextAction      `json:"next_actions,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
	cause       error
}

// NewError builds a typed error with the steps the caller may take next.
func NewError(code ErrorCode, message string, next ...NextAction) *Error {
	return &Error{Code: code, Message: message, NextActions: next}
}

// Error renders "CODE: message" without the cause.
func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// WithCause attaches the underlying error for errors.Is and errors.As.
func (e *Error) WithCause(cause error) *Error {
	e.cause = cause
	return e
}

// Unwrap returns the underlying error, if any.
func (e *Error) Unwrap() error { return e.cause }

// CodeOf returns the code carried by err, "" for nil and CodeInternal for an
// error that carries none.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// HasCode reports whether err carries exactly code.
func HasCode(err error, code ErrorCode) bool { return err != nil && CodeOf(err) == code }

// invalid builds the INVALID_ARGUMENT error used by the validation helpers.
func invalid(format string, args ...any) *Error {
	return NewError(CodeInvalidArgument, fmt.Sprintf(format, args...))
}
