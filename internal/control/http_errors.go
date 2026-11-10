package control

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
)

// errorEnvelope is the body of every API failure: the typed error of
// pkg/core, with its code, message and the next actions open to the caller.
type errorEnvelope struct {
	Error *core.Error `json:"error"`
}

// statusFor maps an error code to its HTTP status. A caller branches on the
// code in the body; the status is for generic HTTP tooling.
func statusFor(code core.ErrorCode) int {
	switch code {
	case core.CodeInvalidArgument:
		return http.StatusBadRequest
	case core.CodeUnauthenticated:
		return http.StatusUnauthorized
	case core.CodePermissionDenied, core.CodeApprovalRequired, core.CodeStepUpRequired:
		return http.StatusForbidden
	case core.CodeNotFound:
		return http.StatusNotFound
	case core.CodeAlreadyExists, core.CodeConflict, core.CodeInvalidTransition,
		core.CodeGuardFailed, core.CodeLeaseHeld, core.CodeStaleFencingToken, core.CodeReplayed:
		return http.StatusConflict
	case core.CodeExpired:
		return http.StatusGone
	case core.CodeRateLimited, core.CodeBudgetExceeded, core.CodeLimitExceeded:
		return http.StatusTooManyRequests
	case core.CodeNotSupported:
		return http.StatusNotImplemented
	case core.CodeTimeout:
		return http.StatusGatewayTimeout
	case core.CodeUnavailable, core.CodeFrozen, core.CodePolicyHold, core.CodeReleaseHold, core.CodeBudgetUnconfirmed:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// writeAPIError answers with the typed error. An error without a code is
// reported as INTERNAL with a fixed message; the original goes to the log
// only, so a driver or file-system message never reaches the caller.
func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		typed = core.NewError(core.CodeInternal, "the request failed; see the server log")
	} else if typed.Code == core.CodeInternal {
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeJSON(w, statusFor(typed.Code), errorEnvelope{Error: typed})
}

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The client may be gone; there is nobody to tell about a failed write.
	_, _ = w.Write(append(body, '\n'))
}
