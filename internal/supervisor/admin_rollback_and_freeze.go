package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RollbackPath is the admin route of an Owner-approved rollback to the last
// known good release.
const RollbackPath = "/rollback"

// approvalRefPattern is the form of an approval request ID.
var approvalRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// RollbackRequest asks the supervisor to restore one unit to its last known
// good release. ApprovalRef names the Owner's approval request; the
// supervisor, not the caller, verifies and spends the approval.
type RollbackRequest struct {
	FactoryID   core.FactoryID `json:"factory_id"`
	Unit        string         `json:"unit"`
	Reason      string         `json:"reason"`
	ApprovalRef string         `json:"approval_ref"`
}

// Validate requires a factory, a unit, a reason and an approval reference.
func (r RollbackRequest) Validate() error {
	if err := r.FactoryID.Validate(); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(r.Unit) == "" || strings.TrimSpace(r.Reason) == "":
		return invalid("a rollback needs the unit and a reason")
	case len(r.Unit) > maxStopTextLength || len(r.Reason) > maxStopTextLength:
		return invalid("the unit and the reason of a rollback are limited to %d bytes", maxStopTextLength)
	case !approvalRefPattern.MatchString(r.ApprovalRef):
		return newError(core.CodeApprovalRequired, "a rollback needs the Owner's approval")
	}
	return nil
}

// RollbackReport is what a rollback did, with what runs afterwards as read
// from the running system.
type RollbackReport struct {
	Unit       string             `json:"unit"`
	From       core.ReleaseDigest `json:"from,omitempty"`
	RestoredTo core.ReleaseDigest `json:"restored_to"`
	Observed   core.ReleaseDigest `json:"observed"`
	Healthy    bool               `json:"healthy"`
	At         time.Time          `json:"at"`
}

// Rollbacker performs an approved rollback on behalf of the admin socket.
type Rollbacker func(ctx context.Context, req RollbackRequest) (RollbackReport, error)

// FreezeStatus says whether an emergency stop is in force.
type FreezeStatus struct {
	Frozen    bool      `json:"frozen"`
	StopID    string    `json:"stop_id,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	StoppedAt time.Time `json:"stopped_at,omitzero"`
}

// FreezeStatusReader reads the emergency stop.
type FreezeStatusReader func(ctx context.Context) (FreezeStatus, error)

// rollbackHandler decodes a rollback request strictly and runs it to the end
// even when the client goes away.
func rollbackHandler(rollback Rollbacker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RollbackRequest
		if !decodeAdminRequest(w, r, &req) {
			return
		}
		if err := req.Validate(); err != nil {
			writeAdminError(w, err)
			return
		}
		report, err := rollback(context.WithoutCancel(r.Context()), req)
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeAdminJSON(w, http.StatusOK, report)
	})
}

// freezeStatusHandler answers whether an emergency stop is in force.
func freezeStatusHandler(read FreezeStatusReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := read(r.Context())
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeAdminJSON(w, http.StatusOK, status)
	})
}

// decodeAdminRequest decodes one bounded JSON body with no unknown field,
// answering the refusal itself when it cannot.
func decodeAdminRequest(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxStopRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeAdminError(w, invalid("the request is not readable"))
		return false
	}
	return true
}
