package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/ui"
)

// Page names, as the ui package knows them.
const (
	pageSignIn    = ui.PageDashSignIn
	pageError     = ui.PageDashError
	pageResult    = ui.PageDashResult
	pageOverview  = ui.PageDashOverview
	pageWorkboard = ui.PageDashWorkboard
	pageRuns      = ui.PageDashRuns
	pageTelemetry = ui.PageDashTelemetry
	pageApprovals = ui.PageDashApprovals
	pageApproval  = ui.PageDashApproval
	pageProduct   = ui.PageDashProduct
	pageSecurity  = ui.PageDashSecurity
	pageSetup     = ui.PageDashSetup
	pageBlocked   = ui.PageDashBlocked
)

// Frame is what the shared layout reads on every page.
type Frame struct {
	Nav      string
	LoggedIn bool
	CSRF     string
	Who      string
	// StepUpProviders are the providers the person can confirm their
	// identity through; a step-up button is offered for each, and none when
	// no provider can force a fresh sign-in for this person.
	StepUpProviders []string
	ReturnTo        string
}

func (h *handler) frame(s *session, nav, returnTo string) Frame {
	return Frame{Nav: nav, LoggedIn: true, CSRF: s.csrf, Who: s.who, StepUpProviders: s.stepUp, ReturnTo: auth.LocalReturnPath(returnTo)}
}

type signInView struct {
	Frame
	Providers []string
	ReturnTo  string
	Message   string
}

type errorView struct {
	Frame
	Code    core.ErrorCode
	Message string
	Next    []NextView
}

// NextView is a next action ready to show: a link to the page that offers it
// when there is one.
type NextView struct {
	Action      string
	Description string
	Link        string
	Params      string
}

// render writes a finished page. Rendering to memory first means a failure
// halfway never sends a truncated page.
func (h *handler) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	body, err := h.pages.Render(page, data)
	if err != nil {
		h.log.Error("dashboard render failed", "path", r.URL.Path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// The client may be gone; there is nobody to tell about a failed write.
	_, _ = w.Write(body)
}

// fail shows a typed error on the error page. The message of an internal
// error is replaced, so a driver or file-system text never reaches a person.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, s *session, err error) {
	typed := h.typed(r, err)
	frame := Frame{}
	if s != nil {
		frame = h.frame(s, "", "/")
	}
	h.render(w, r, statusFor(typed.Code), pageError, errorView{Frame: frame, Code: typed.Code, Message: typed.Message, Next: nextViews(typed.NextActions)})
}

// typed returns the caller-safe form of err and logs the cause of an
// internal failure.
func (h *handler) typed(r *http.Request, err error) *core.Error {
	var typed *core.Error
	if errors.As(err, &typed) && typed.Code != core.CodeInternal && typed.Code != core.CodeTampered {
		return typed
	}
	h.log.Error("dashboard request failed", "path", r.URL.Path, "error", err)
	out := core.NewError(core.CodeInternal, "internal error; see the server log")
	if typed != nil {
		out.NextActions = typed.NextActions
	}
	return out
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.fail(w, r, nil, core.NewError(core.CodeNotFound, "there is no such page"))
		return
	}
	s, err := h.identify(r)
	if err != nil {
		h.refuse(w, r, err)
		return
	}
	h.fail(w, r, s, core.NewError(core.CodeNotFound, "there is no such page"))
}

// statusFor is the HTTP status of a typed error code.
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
	case core.CodeExpired:
		return http.StatusGone
	case core.CodePolicyHold, core.CodeReleaseHold, core.CodeFrozen:
		return http.StatusLocked
	case core.CodeBudgetExceeded:
		return http.StatusPaymentRequired
	case core.CodeLimitExceeded, core.CodeRateLimited:
		return http.StatusTooManyRequests
	case core.CodeNotSupported:
		return http.StatusNotImplemented
	case core.CodeUnavailable:
		return http.StatusServiceUnavailable
	case core.CodeTimeout:
		return http.StatusGatewayTimeout
	case core.CodeInternal, core.CodeTampered:
		return http.StatusInternalServerError
	case core.CodeSignatureInvalid:
		return http.StatusUnprocessableEntity
	}
	return http.StatusConflict
}

func nextViews(next []core.NextAction) []NextView {
	out := make([]NextView, 0, len(next))
	for _, n := range next {
		out = append(out, NextView{Action: n.Action, Description: n.Description, Link: nextLink(n), Params: paramsText(n.Params)})
	}
	return out
}

// nextLink is the dashboard page that offers a next action, or empty.
func nextLink(n core.NextAction) string {
	issue := url.QueryEscape(n.Params["issue"])
	switch n.Action {
	case "factory.status":
		return PathOverview
	case "approvals.list":
		return PathApprovals
	case "approvals.get":
		return approvalLink(n.Params["id"])
	case "approvals.decide":
		return approvalLink(n.Params["request_id"])
	case "runs.explain":
		return PathBlocked + queryIf("issue", issue)
	case "runs.inspect":
		return PathRuns + queryIf("issue", issue)
	case "budget.get":
		return PathSecurity
	}
	return ""
}

func approvalLink(id string) string {
	if id == "" {
		return PathApprovals
	}
	return PathApprovals + "/" + url.PathEscape(id)
}

func queryIf(key, escaped string) string {
	if escaped == "" {
		return ""
	}
	return "?" + key + "=" + escaped
}

func paramsText(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+p[k])
	}
	return strings.Join(parts, ", ")
}
