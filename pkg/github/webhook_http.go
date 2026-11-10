package github

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ServeHTTP answers a delivery. It reads the body once, verifies it, hands
// the event to the sink and answers 2xx without doing any further work.
//
//	401 no or bad signature      403 other installation or repository
//	400 malformed delivery       413 body too large
//	200 ignored or duplicate     202 accepted
//	503 sink failed or the same delivery is being handled right now
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		reply(w, http.StatusMethodNotAllowed, "error", "deliveries are POST requests")
		return
	}
	if mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		reply(w, http.StatusUnsupportedMediaType, "error", "deliveries must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, r.cfg.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, "error", "the delivery is too large")
			return
		}
		reply(w, http.StatusBadRequest, "error", "the delivery body could not be read")
		return
	}
	ev, err := r.Verify(req.Header.Get(SignatureHeader), req.Header.Get(EventHeader), req.Header.Get(DeliveryHeader), body)
	if err != nil {
		replyError(w, err)
		return
	}
	duplicate, busy := r.ledger.begin(ev.DeliveryID)
	switch {
	case duplicate:
		reply(w, http.StatusOK, "duplicate", "this delivery was already handled")
		return
	case busy:
		w.Header().Set("Retry-After", "1")
		reply(w, http.StatusServiceUnavailable, "error", "this delivery is being handled; retry")
		return
	}
	sinkErr := r.sink(req.Context(), ev)
	r.ledger.finish(ev.DeliveryID, sinkErr == nil)
	if sinkErr != nil {
		w.Header().Set("Retry-After", "5")
		reply(w, http.StatusServiceUnavailable, "error", "the event could not be stored; retry")
		return
	}
	reply(w, http.StatusAccepted, "accepted", "")
}

// replyError maps a Verify failure to its status.
func replyError(w http.ResponseWriter, err error) {
	code := core.CodeOf(err)
	switch code {
	case core.CodeNotSupported:
		reply(w, http.StatusOK, "ignored", "")
	case core.CodeUnauthenticated, core.CodeSignatureInvalid:
		reply(w, http.StatusUnauthorized, string(code), err.Error())
	case core.CodePermissionDenied:
		reply(w, http.StatusForbidden, string(code), err.Error())
	case core.CodeInvalidArgument:
		reply(w, http.StatusBadRequest, string(code), err.Error())
	default:
		reply(w, http.StatusInternalServerError, string(core.CodeInternal), "the delivery could not be verified")
	}
}

func reply(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The caller is GitHub or a probe; a failed write has nobody to tell.
	_ = json.NewEncoder(w).Encode(map[string]string{"status": kind, "message": message})
}
