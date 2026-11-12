package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// EmergencyStopPath is the admin route that stops the factory. It is the one
// route of the admin socket that acts, and it can only make the factory
// stop: lifting a stop is not offered here.
const EmergencyStopPath = "/emergency-stop"

// maxStopRequestBytes bounds an emergency stop request.
const maxStopRequestBytes = 16 << 10

// maxStopTextLength bounds the actor and the reason of a stop.
const maxStopTextLength = 512

// EmergencyStopRequest asks the supervisor to stop the factory.
type EmergencyStopRequest struct {
	// FactoryID names the factory to stop. The admin socket refuses a stop
	// for any other factory, and one that names none: two factories on one
	// host share the default admin address.
	FactoryID core.FactoryID `json:"factory_id,omitempty"`
	// Actor names who asked, for the audit trail.
	Actor string `json:"actor"`
	// Reason says why, for the people who lift the stop.
	Reason string `json:"reason"`
}

// Validate requires an actor and a reason of bounded length.
func (r EmergencyStopRequest) Validate() error {
	actor, reason := strings.TrimSpace(r.Actor), strings.TrimSpace(r.Reason)
	switch {
	case actor == "" || reason == "":
		return invalid("an emergency stop needs the actor who asks and a reason")
	case len(actor) > maxStopTextLength || len(reason) > maxStopTextLength:
		return invalid("the actor and the reason of an emergency stop are limited to %d bytes", maxStopTextLength)
	}
	return nil
}

// EmergencyStopReport is what an emergency stop did. Failures lists each
// part that could not be done; a stop with failures is still in force for
// everything else.
type EmergencyStopReport struct {
	StopID    string    `json:"stop_id"`
	StoppedAt time.Time `json:"stopped_at"`
	// PublishingFrozen says no release step may promote or publish.
	PublishingFrozen bool `json:"publishing_frozen"`
	// ClaimsHeld are the issues no run can claim while the stop holds.
	ClaimsHeld []core.IssueRef `json:"claims_held,omitempty"`
	// RunsCancelled and SecretsRevoked name the runs that were stopped.
	RunsCancelled  []core.RunID `json:"runs_cancelled,omitempty"`
	SecretsRevoked []core.RunID `json:"secrets_revoked,omitempty"`
	Failures       []string     `json:"failures,omitempty"`
}

// EmergencyStopper performs an emergency stop on behalf of the admin socket.
type EmergencyStopper func(ctx context.Context, req EmergencyStopRequest) (EmergencyStopReport, error)

// adminError is the body of a refused admin request.
type adminError struct {
	Code    core.ErrorCode `json:"code"`
	Message string         `json:"message"`
}

// emergencyStopHandler decodes a stop request strictly and runs it. The stop
// is not abandoned when the client goes away: a stop that was asked for is
// carried out.
func emergencyStopHandler(stop EmergencyStopper, served func() core.FactoryID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req EmergencyStopRequest
		if !decodeAdminRequest(w, r, &req) {
			return
		}
		if err := req.Validate(); err != nil {
			writeAdminError(w, err)
			return
		}
		if err := checkStopFactory(req.FactoryID, served()); err != nil {
			writeAdminError(w, err)
			return
		}
		report, err := stop(context.WithoutCancel(r.Context()), req)
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeAdminJSON(w, http.StatusOK, report)
	})
}

// checkStopFactory refuses a stop that does not name the factory this
// supervisor serves. The admin address of Standalone mode is a fixed
// loopback port, so the stop of another factory on the same host could
// otherwise reach this one.
func checkStopFactory(asked, served core.FactoryID) error {
	switch {
	case asked == "":
		return invalid("an emergency stop must name the factory it stops (factory_id); this supervisor serves %s", served)
	case asked != served:
		return newError(core.CodeConflict, "this supervisor serves factory %s, not %s; nothing was stopped", served, asked)
	}
	return nil
}

// writeAdminJSON writes one JSON answer. A failed write means the client
// went away; there is nobody to tell.
func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAdminError answers a refusal with its code and message.
func writeAdminError(w http.ResponseWriter, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		typed = core.NewError(core.CodeInternal, "the request failed")
	}
	status := http.StatusInternalServerError
	switch typed.Code {
	case core.CodeInvalidArgument:
		status = http.StatusBadRequest
	case core.CodeConflict:
		status = http.StatusConflict
	case core.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	writeAdminJSON(w, status, adminError{Code: typed.Code, Message: typed.Message})
}
