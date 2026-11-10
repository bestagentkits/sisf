package api

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// pageData feeds the verification page. Everything in it is escaped by
// html/template for the place it lands in.
type pageData struct {
	Message  string
	Failed   bool
	Done     bool
	UserCode string
	Name     string
	Scopes   []string
	Expires  time.Time
	SignedIn bool
	CSRF     string
	Links    []providerLink
}

// providerLink is where the page sends a person to sign in or to confirm
// their identity again.
type providerLink struct{ Name, SignIn, StepUp string }

var verifyPage = template.Must(template.New("verify").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Sign the command line in</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body><main>
<h1>Sign the command line in</h1>
{{if .Message}}<p role="{{if .Failed}}alert{{else}}status{{end}}">{{.Message}}</p>{{end}}
{{if and .UserCode (not .Done)}}
<p>A command line asks to act as you{{if .Name}} (<strong>{{.Name}}</strong>){{end}} until {{.Expires.Format "15:04 MST"}}.
Check that this code is the one the terminal shows: <strong>{{.UserCode}}</strong></p>
<p>It may use these scopes:</p><ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul>
{{if .SignedIn}}
<form method="post" action="` + DeviceApprovePath + `">
<input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="user_code" value="{{.UserCode}}">
<button type="submit">Approve this command line</button></form>
<p>Approving needs a recent identity confirmation. If it is refused for that reason, confirm and approve again:</p>
{{range .Links}}<form method="post" action="{{.StepUp}}"><input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<button type="submit">Confirm identity with {{.Name}}</button></form>{{end}}
{{else}}{{range .Links}}<p><a href="{{.SignIn}}">Sign in with {{.Name}}</a></p>{{end}}{{end}}
{{end}}
</main></body></html>
`))

// deviceVerify is the page a person opens from the URL the CLI printed.
func (h *handler) deviceVerify(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("user_code")
	data := pageData{}
	status := http.StatusOK
	g, err := h.devices.lookup(code)
	switch {
	case code == "":
		data.Message, status = "Open the link the terminal printed, or add ?user_code= with the code it shows.", http.StatusBadRequest
	case err != nil:
		data.Message, data.Failed, status = pageMessage(err), true, statusFor(err)
	case g.approved:
		data.Message, data.Done = "This login request was already approved.", true
	default:
		data = h.fillGrant(r, data, g)
	}
	h.writePage(w, status, data)
}

// fillGrant adds the request and the viewer's sign-in state to the page.
func (h *handler) fillGrant(r *http.Request, data pageData, g grant) pageData {
	data.UserCode, data.Name, data.Scopes, data.Expires = formatUserCode(g.userCode), g.name, g.scopes, g.expires
	data.CSRF, data.SignedIn = h.sessionCSRF(r)
	back := url.QueryEscape(DeviceVerifyPath + "?user_code=" + url.QueryEscape(data.UserCode))
	for _, name := range h.cfg.LoginProviders {
		p := url.PathEscape(name)
		data.Links = append(data.Links, providerLink{Name: name,
			SignIn: "/auth/login/" + p + "?return_to=" + back, StepUp: "/auth/step-up/" + p + "?return_to=" + back})
	}
	return data
}

// renderApproval answers a form post of the approve button with a page. A
// failure keeps the request on the page, so the person can sign in again or
// confirm their identity and retry.
func (h *handler) renderApproval(w http.ResponseWriter, r *http.Request, code string, err error) {
	if err == nil {
		h.writePage(w, http.StatusOK, pageData{Message: "Approved. Return to the terminal: it signs in by itself.", Done: true})
		return
	}
	h.logFailure(err)
	data := pageData{Message: pageMessage(err), Failed: true}
	if view, lerr := h.devices.lookup(code); lerr == nil && !view.approved {
		data = h.fillGrant(r, data, view)
	}
	h.writePage(w, statusFor(err), data)
}

func (h *handler) writePage(w http.ResponseWriter, status int, data pageData) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = verifyPage.Execute(w, data)
}

// sessionCSRF reads the CSRF token of the viewer's session from the
// authentication service's own session view, so the page never touches the
// session store. signedIn is false when no live session answers.
func (h *handler) sessionCSRF(r *http.Request) (csrf string, signedIn bool) {
	probe, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "/auth/session", nil)
	if err != nil {
		return "", false
	}
	for _, c := range r.Cookies() {
		probe.AddCookie(c)
	}
	rec := &memoryResponse{header: http.Header{}, status: http.StatusOK}
	h.cfg.Identity.Handler().ServeHTTP(rec, probe)
	var view struct {
		CSRFToken string `json:"csrf_token"`
	}
	if rec.status != http.StatusOK || json.Unmarshal(rec.body, &view) != nil || view.CSRFToken == "" {
		return "", false
	}
	return view.CSRFToken, true
}

// memoryResponse collects the answer of an in-process handler call.
type memoryResponse struct {
	header http.Header
	status int
	body   []byte
}

func (m *memoryResponse) Header() http.Header { return m.header }
func (m *memoryResponse) WriteHeader(s int)   { m.status = s }
func (m *memoryResponse) Write(b []byte) (int, error) {
	m.body = append(m.body, b...)
	return len(b), nil
}

// pageMessage is the caller-safe text of a failure.
func pageMessage(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) && !internalCode(typed.Code) {
		return typed.Message
	}
	return "something went wrong; try again"
}
