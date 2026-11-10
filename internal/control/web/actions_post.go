package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Limits on what a form may carry.
const (
	maxFormBytes  = 16 << 10
	maxReasonLen  = 1000
	maxApprovalID = 256
)

// formAction turns the fields of a form into the input of one action.
type formAction func(url.Values) (any, error)

// formActions are the actions the dashboard posts, and nothing else: a form
// cannot name another action. Each is the same action REST serves, run by the
// same service, so a denial or a missing approval is identical.
var formActions = map[string]formAction{
	actionStop:                   reasonInput,
	actionRollback:               reasonInput,
	approvalactions.ActionDecide: decideInput,
	actionSampleIssue:            func(url.Values) (any, error) { return nil, nil },
}

type resultView struct {
	Frame
	Title      string
	Action     string
	Failed     bool
	Code       core.ErrorCode
	Message    string
	Next       []NextView
	Details    []detail
	Output     string
	ApprovalID string
	StepUp     bool
}

type detail struct{ Key, Value string }

// postAction runs one action for a form POST. The session and the CSRF token
// were checked by the guard before this runs.
func (h *handler) postAction(w http.ResponseWriter, r *http.Request, s *session) {
	name := r.PathValue("name")
	build, ok := formActions[name]
	if !ok {
		h.fail(w, r, s, core.NewError(core.CodeNotFound, "the dashboard offers no form for this action"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, s, core.NewError(core.CodeInvalidArgument, "the form could not be read"))
		return
	}
	form := r.PostForm
	returnTo := localPath(form.Get("return_to"))
	view := resultView{Frame: h.frame(s, "", returnTo), Title: name, Action: name}
	approvalID := strings.TrimSpace(form.Get("approval_id"))
	input, err := build(form)
	if err == nil && (len(approvalID) > maxApprovalID || strings.ContainsAny(approvalID, "\r\n\x00")) {
		err = core.NewError(core.CodeInvalidArgument, "the approval id is too long or holds control characters")
	}
	var res core.ActionResult
	if err == nil {
		res, err = h.invoke(r, s, name, input, approvalID)
	}
	if err != nil {
		h.renderFailure(w, r, view, err)
		return
	}
	view.Output = pretty(res.Output)
	h.render(w, r, http.StatusOK, pageResult, view)
}

// renderFailure shows the typed error exactly as the other surfaces carry
// it: its code, message and next actions. A missing approval links to the
// request that was filed; a missing step-up offers the confirmation.
func (h *handler) renderFailure(w http.ResponseWriter, r *http.Request, view resultView, err error) {
	typed := h.typed(r, err)
	view.Failed, view.Code, view.Message, view.Next = true, typed.Code, typed.Message, nextViews(typed.NextActions)
	view.ApprovalID = typed.Details["approval_id"]
	view.StepUp = typed.Code == core.CodeStepUpRequired
	for _, key := range []string{"approval_id", "subject_digest", "policy_revision", "expires_at"} {
		if v := typed.Details[key]; v != "" {
			view.Details = append(view.Details, detail{Key: key, Value: v})
		}
	}
	h.render(w, r, statusFor(typed.Code), pageResult, view)
}

func pretty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out strings.Builder
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if enc.Encode(v) != nil {
		return ""
	}
	return out.String()
}

func reasonInput(f url.Values) (any, error) {
	reason := strings.TrimSpace(f.Get("reason"))
	if reason == "" || len(reason) > maxReasonLen {
		return nil, core.NewError(core.CodeInvalidArgument, "give a reason of up to 1000 characters")
	}
	return map[string]string{"reason": reason}, nil
}

// decideInput builds an approval decision. The nonce, digest and policy
// revision are echoed as shown; the approval store checks them.
func decideInput(f url.Values) (any, error) {
	in := map[string]any{}
	for _, key := range []string{"request_id", "nonce", "subject_digest", "policy_revision"} {
		v := strings.TrimSpace(f.Get(key))
		if v == "" || len(v) > maxApprovalID {
			return nil, core.NewError(core.CodeInvalidArgument, "the form is missing "+key)
		}
		in[key] = v
	}
	decision := approvals.Decision(f.Get("decision"))
	if !decision.Valid() {
		return nil, core.NewError(core.CodeInvalidArgument, "choose approve, request changes or reject")
	}
	in["decision"] = decision
	if reason := strings.TrimSpace(f.Get("reason")); reason != "" {
		if len(reason) > maxReasonLen {
			return nil, core.NewError(core.CodeInvalidArgument, "the reason is longer than 1000 characters")
		}
		in["reason"] = reason
	}
	conditions := map[string]any{}
	if raw := strings.TrimSpace(f.Get("budget_cap")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return nil, core.NewError(core.CodeInvalidArgument, "the budget cap must be a whole number of millionths, zero or more")
		}
		conditions["budget_cap"] = n
	}
	if raw := strings.TrimSpace(f.Get("expires_at")); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "the expiry must be an RFC 3339 time such as 2026-11-12T09:00:00Z")
		}
		conditions["expires_at"] = at.UTC()
	}
	if len(conditions) > 0 {
		in["conditions"] = conditions
	}
	return in, nil
}
