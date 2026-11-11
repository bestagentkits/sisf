package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
)

// writeError answers with the typed error and its HTTP status. The message
// is the caller-safe one; the cause is never written.
func writeError(w http.ResponseWriter, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		typed = core.NewError(core.CodeInternal, "internal error")
	}
	status := http.StatusInternalServerError
	switch typed.Code {
	case core.CodeUnauthenticated:
		w.Header().Set("WWW-Authenticate", `Bearer realm="sisf"`)
		status = http.StatusUnauthorized
	case core.CodePermissionDenied, core.CodeStepUpRequired:
		status = http.StatusForbidden
	case core.CodeInvalidArgument:
		status = http.StatusBadRequest
	case core.CodeNotFound:
		status = http.StatusNotFound
	case core.CodeRateLimited:
		status = http.StatusTooManyRequests
	case core.CodeUnavailable:
		status = http.StatusBadGateway
	case core.CodeNotSupported:
		status = http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]any{"error": core.Error{Code: typed.Code, Message: typed.Message, NextActions: typed.NextActions}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
