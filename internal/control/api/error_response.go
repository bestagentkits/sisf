package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
)

// statusByCode maps every typed error code to its HTTP status. A test keeps
// it in step with core.ErrorCodes, so a new code cannot fall through to 500.
var statusByCode = map[core.ErrorCode]int{
	core.CodeInvalidArgument:   http.StatusBadRequest,
	core.CodeNotFound:          http.StatusNotFound,
	core.CodeAlreadyExists:     http.StatusConflict,
	core.CodeUnauthenticated:   http.StatusUnauthorized,
	core.CodePermissionDenied:  http.StatusForbidden,
	core.CodeApprovalRequired:  http.StatusForbidden,
	core.CodeStepUpRequired:    http.StatusForbidden,
	core.CodeConflict:          http.StatusConflict,
	core.CodeInvalidTransition: http.StatusConflict,
	core.CodeGuardFailed:       http.StatusConflict,
	core.CodeLeaseHeld:         http.StatusConflict,
	core.CodeStaleFencingToken: http.StatusConflict,
	core.CodeReplayed:          http.StatusConflict,
	core.CodeExpired:           http.StatusGone,
	core.CodeSignatureInvalid:  http.StatusUnprocessableEntity,
	core.CodeTampered:          http.StatusInternalServerError,
	core.CodePolicyHold:        http.StatusLocked,
	core.CodeBudgetUnconfirmed: http.StatusConflict,
	core.CodeBudgetExceeded:    http.StatusPaymentRequired,
	core.CodeLimitExceeded:     http.StatusTooManyRequests,
	core.CodeRateLimited:       http.StatusTooManyRequests,
	core.CodeNotSupported:      http.StatusNotImplemented,
	core.CodeReleaseHold:       http.StatusLocked,
	core.CodeFrozen:            http.StatusLocked,
	core.CodeUnavailable:       http.StatusServiceUnavailable,
	core.CodeTimeout:           http.StatusGatewayTimeout,
	core.CodeInternal:          http.StatusInternalServerError,
}

// internalCode reports whether the message of an error with this code is
// replaced by a fixed one, because the failure is the factory's own.
func internalCode(c core.ErrorCode) bool { return c == core.CodeInternal || c == core.CodeTampered }

// requestError is a typed error that also fixes the HTTP status, for the
// few failures whose status is more precise than their code's.
type requestError struct {
	status int
	err    *core.Error
}

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// errorBody is the JSON body of every failure.
type errorBody struct {
	Error core.Error `json:"error"`
}

// writeError answers with the typed error, its HTTP status and a JSON body
// carrying the code, the message and the next actions. The cause is never
// written, and an error without a code is reported as INTERNAL.
func writeError(w http.ResponseWriter, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		typed = core.NewError(core.CodeInternal, "internal error")
	}
	out := core.Error{Code: typed.Code, Message: typed.Message, NextActions: typed.NextActions, Details: typed.Details}
	if internalCode(typed.Code) {
		out.Message, out.Details = "internal error; see the server log", nil
	}
	status := statusFor(err)
	switch typed.Code {
	case core.CodeUnauthenticated:
		w.Header().Set("WWW-Authenticate", `Bearer realm="sisf"`)
	case core.CodeRateLimited:
		if after := typed.Details["retry_after_seconds"]; after != "" {
			w.Header().Set("Retry-After", after)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, errorBody{Error: out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// statusFor is the HTTP status of err: the one it fixes, else its code's.
func statusFor(err error) int {
	var withStatus *requestError
	if errors.As(err, &withStatus) {
		return withStatus.status
	}
	if status, known := statusByCode[core.CodeOf(err)]; known {
		return status
	}
	return http.StatusInternalServerError
}
