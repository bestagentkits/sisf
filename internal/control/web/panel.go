package web

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Panel is the outcome of one read action as a page shows it: the output, or
// why there is none. A page never shows a panel it could not fill with real
// data; it shows the reason instead.
type Panel struct {
	Output      json.RawMessage
	Unavailable bool
	Code        core.ErrorCode
	Message     string
	Next        []NextView
}

// OK reports whether the panel holds an output.
func (p Panel) OK() bool { return !p.Unavailable && p.Code == "" }

// registered reports whether the factory offers the action.
func (h *handler) registered(name string) bool {
	return slices.ContainsFunc(h.cfg.Actions.Specs(), func(s core.ActionSpec) bool { return s.Name == name })
}

// invoke runs an action for the signed-in person on the dashboard surface.
func (h *handler) invoke(r *http.Request, s *session, name string, input any, approvalID string) (core.ActionResult, error) {
	req := core.ActionRequest{Actor: s.actor, ApprovalID: approvalID}
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return core.ActionResult{}, core.NewError(core.CodeInternal, "the input cannot be encoded").WithCause(err)
		}
		req.Input = raw
	}
	return h.cfg.Actions.Invoke(r.Context(), name, req)
}

// read runs a read action and turns the answer into a panel. An action the
// factory does not offer, and one that answers NOT_SUPPORTED, are "not
// available yet"; any other failure shows its typed code.
func (h *handler) read(r *http.Request, s *session, name string, input any) Panel {
	if !h.registered(name) {
		return Panel{Unavailable: true, Message: "This factory offers no " + name + " action to the control zone.",
			Next: nextViews([]core.NextAction{{Action: "factory.status", Description: "see what the factory offers"}})}
	}
	res, err := h.invoke(r, s, name, input, "")
	if err != nil {
		return h.failedPanel(r, err)
	}
	return Panel{Output: res.Output}
}

func (h *handler) failedPanel(r *http.Request, err error) Panel {
	typed := h.typed(r, err)
	next := nextViews(typed.NextActions)
	if typed.Code == core.CodeNotSupported {
		return Panel{Unavailable: true, Message: typed.Message, Next: next}
	}
	return Panel{Code: typed.Code, Message: typed.Message, Next: next}
}

// decode reads the output of an OK panel into T. An output that does not
// fit is reported as an internal failure of the panel.
func decode[T any](p Panel) (T, Panel) {
	var out T
	if !p.OK() {
		return out, p
	}
	if len(p.Output) == 0 {
		return out, p
	}
	if err := json.Unmarshal(p.Output, &out); err != nil {
		return out, Panel{Code: core.CodeInternal, Message: "the answer could not be read"}
	}
	return out, p
}
