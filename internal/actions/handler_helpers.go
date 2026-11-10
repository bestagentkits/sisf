package actions

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DecodeInput reads the input of a call into T. Unknown fields are refused,
// so a misspelt parameter is reported instead of silently ignored. An empty
// input decodes to the zero T.
func DecodeInput[T any](req core.ActionRequest) (T, error) {
	var out T
	if len(bytes.TrimSpace(req.Input)) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(req.Input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, core.NewError(core.CodeInvalidArgument, "the input does not match the action's input schema").WithCause(err)
	}
	return out, nil
}

// JSONResult returns v as the output of a call.
func JSONResult(v any) (core.ActionResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return core.ActionResult{}, core.NewError(core.CodeInternal, "the action output cannot be encoded").WithCause(err)
	}
	return core.ActionResult{Output: raw}, nil
}

// NotSupported returns an action that refuses every call with NOT_SUPPORTED
// and next. It stands for an action whose implementation is not wired in
// this factory, so a surface can list it and answer honestly instead of
// faking a result. Register it with Registry.RegisterFallback.
func NotSupported(spec core.ActionSpec, reason string, next ...core.NextAction) core.Action {
	return core.Action{Spec: spec, Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
		return core.ActionResult{}, core.NewError(core.CodeNotSupported, reason, next...)
	}}
}

// Schema returns a JSON Schema document written as text, for an ActionSpec.
func Schema(text string) json.RawMessage { return json.RawMessage(text) }
