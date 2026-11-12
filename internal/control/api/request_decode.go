package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// idempotencyHeader is an alternative to the idempotency_key body field.
const idempotencyHeader = "Idempotency-Key"

// maxTokenLen bounds the idempotency key and approval reference.
const maxTokenLen = 256

// callBody is the request body of an action call, exactly the fields the
// OpenAPI document declares. The actor is not among them: it comes from the
// authentication layer.
type callBody struct {
	Input          json.RawMessage `json:"input"`
	DryRun         bool            `json:"dry_run"`
	IdempotencyKey string          `json:"idempotency_key"`
	ApprovalID     string          `json:"approval_id"`
}

// decodeCall reads and validates the body of an action call. An empty body
// is a call with no input.
func decodeCall(w http.ResponseWriter, r *http.Request, limit int64) (callBody, error) {
	var call callBody
	if err := decodeBody(w, r, limit, &call); err != nil {
		return call, err
	}
	if string(call.Input) == "null" {
		call.Input = nil
	}
	if header := r.Header.Get(idempotencyHeader); header != "" {
		if call.IdempotencyKey != "" && call.IdempotencyKey != header {
			return call, invalid("the idempotency key in the header and in the body differ")
		}
		call.IdempotencyKey = header
	}
	for name, value := range map[string]string{"idempotency_key": call.IdempotencyKey, "approval_id": call.ApprovalID} {
		if len(value) > maxTokenLen || strings.ContainsAny(value, "\r\n\x00") {
			return call, invalid(name + " is too long or holds control characters")
		}
	}
	return call, nil
}

// decodeBody reads one JSON object of at most limit bytes into dst, refusing
// unknown fields and trailing data. An empty body leaves dst untouched.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return &requestError{status: http.StatusRequestEntityTooLarge,
			err: core.NewError(core.CodeLimitExceeded, "the request body is larger than the allowed size")}
	case err != nil:
		return invalid("the request body could not be read")
	case len(strings.TrimSpace(string(raw))) == 0:
		return nil
	}
	if mt, _, perr := mime.ParseMediaType(r.Header.Get("Content-Type")); perr != nil || mt != "application/json" {
		return &requestError{status: http.StatusUnsupportedMediaType, err: core.NewError(core.CodeInvalidArgument, "send the body as application/json")}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalid("the request body is not a valid call: " + err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalid("the request body holds more than one JSON value")
	}
	return nil
}

func invalid(message string) *core.Error { return core.NewError(core.CodeInvalidArgument, message) }
