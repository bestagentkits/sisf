package oauthas

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
)

// consentPage asks the signed-in person whether the client may act for
// them. html/template escapes the client's name, which a registering party
// chose and which is therefore untrusted text.
var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Authorize {{.ClientName}}</title></head>
<body>
<main>
<h1>Allow {{.ClientName}} to act for you?</h1>
{{if .Unverified}}<p><strong>This client registered itself; its name is not verified.</strong></p>{{end}}
<p>It will be sent back to <code>{{.RedirectHost}}</code> and may call <code>{{.Resource}}</code> with these scopes,
until the access token expires or you sign out:</p>
<ul>{{range .Scopes}}<li><code>{{.}}</code></li>{{end}}</ul>
<p>The factory still checks your roles on every action; these scopes never add a permission.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="consent_id" value="{{.ConsentID}}">
<button type="submit" name="decision" value="approve">Allow</button>
<button type="submit" name="decision" value="deny">Deny</button>
</form>
</main>
</body>
</html>
`))

type consentView struct {
	ClientName, RedirectHost, Resource, Action, ConsentID string
	Unverified                                            bool
	Scopes                                                []string
}

func (s *Server) renderConsent(w http.ResponseWriter, req authRequest, consentID string) {
	name := req.client.Name
	if name == "" {
		name = req.client.ID
	}
	host := req.redirectURI
	if u, err := url.Parse(req.redirectURI); err == nil {
		host = u.Host
	}
	var buf bytes.Buffer
	err := consentPage.Execute(&buf, consentView{ClientName: name, RedirectHost: host, Resource: req.resource,
		Action: AuthorizePath, ConsentID: consentID, Unverified: req.client.Dynamic, Scopes: req.scopes})
	if err != nil {
		pageError(w, http.StatusInternalServerError, "the consent page could not be rendered")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// crossOrigin refuses a consent posted from another site.
var crossOrigin = http.NewCrossOriginProtection()

// handleConsent records the person's answer. The consent ID is single-use
// and bound to the session that saw the page, so neither a forged form nor a
// different person can answer for them.
func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	if err := crossOrigin.Check(r); err != nil {
		pageError(w, http.StatusForbidden, "a consent must be given on this site")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil || r.URL.RawQuery != "" {
		pageError(w, http.StatusBadRequest, "the consent form is malformed")
		return
	}
	p, ok := s.takePending(r.PostForm.Get("consent_id"))
	if !ok {
		pageError(w, http.StatusBadRequest, "this consent is unknown, already used or expired; start again from the client")
		return
	}
	actor, err := s.sessionActor(r)
	if err != nil || actor.ID != p.actorID || actor.SessionID != p.session {
		pageError(w, http.StatusForbidden, "the consent belongs to another session; start again from the client")
		return
	}
	req := p.req
	if r.PostForm.Get("decision") != "approve" {
		s.redirectError(w, r, req.redirectURI, req.state, "access_denied", "the person declined")
		return
	}
	code, err := s.issueCode(req, actor)
	if err != nil {
		s.redirectError(w, r, req.redirectURI, req.state, "temporarily_unavailable", messageOf(err))
		return
	}
	q := url.Values{"code": {code}, "iss": {s.cfg.Issuer}}
	if req.state != "" {
		q.Set("state", req.state)
	}
	http.Redirect(w, r, withQuery(req.redirectURI, q), http.StatusFound)
}
