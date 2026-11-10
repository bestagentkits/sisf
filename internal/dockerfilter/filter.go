package dockerfilter

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Filter is the Docker request filter. It is an http.Handler; serve it on a
// listener only the dispatcher can reach. It is safe for concurrent use.
type Filter struct {
	cfg      Config
	log      *slog.Logger
	versions map[string]bool
	images   map[string]bool
	runtimes map[string]bool
}

// New builds a filter, refusing an incomplete or unsafe configuration.
func New(cfg Config) (*Filter, error) {
	cfg, err := cfg.settled()
	if err != nil {
		return nil, err
	}
	f := &Filter{cfg: cfg, log: cfg.Logger, versions: set(cfg.APIVersions), images: set(cfg.Images), runtimes: set(cfg.Runtimes)}
	f.runtimes[""] = true
	return f, nil
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

// denial is a refusal with the status the client sees.
type denial struct {
	status int
	reason string
}

func deny(status int, reason string) *denial { return &denial{status: status, reason: reason} }

// ServeHTTP judges one request and forwards it only when it is allowed.
func (f *Filter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d := f.serve(w, r); d != nil {
		f.refuse(w, r, d)
	}
}

func (f *Filter) serve(w http.ResponseWriter, r *http.Request) *denial {
	if d := checkFraming(r); d != nil {
		return d
	}
	rt, d := f.parseRoute(r)
	if d != nil {
		return d
	}
	switch rt.endpoint {
	case endpointCreate:
		if f.cfg.Role == RoleDeploy {
			return f.createDeploy(w, r, rt)
		}
		return f.create(w, r, rt)
	case endpointList:
		if d := noBody(r); d != nil {
			return d
		}
		return f.list(w, r, rt)
	case endpointPing:
		if d := noBody(r); d != nil {
			return d
		}
		return f.forward(w, r, r.Method, rt.version, "/_ping", nil, nil)
	case endpointInfo:
		if d := noBody(r); d != nil {
			return d
		}
		return f.info(w, r, rt)
	case endpointNetwork:
		if d := noBody(r); d != nil {
			return d
		}
		return f.network(w, r, rt)
	default:
		if d := noBody(r); d != nil {
			return d
		}
		return f.containerAction(w, r, rt)
	}
}

// checkFraming refuses protocol upgrades and hijacking, which the attach and
// exec endpoints rely on, and requests sent in absolute form.
func checkFraming(r *http.Request) *denial {
	switch {
	case r.Header.Get("Upgrade") != "" || headerHasToken(r.Header, "Connection", "upgrade"):
		return deny(http.StatusForbidden, "protocol upgrades are refused")
	case r.URL.Host != "" || r.URL.Scheme != "":
		return deny(http.StatusBadRequest, "requests in absolute form are refused")
	}
	return nil
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// noBody refuses a body on an endpoint that takes none, chunked or not.
func noBody(r *http.Request) *denial {
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		return deny(http.StatusBadRequest, "this endpoint takes no request body")
	}
	return nil
}

// readBody reads a create body of declared length within the limit.
func (f *Filter) readBody(r *http.Request) ([]byte, *denial) {
	switch {
	case len(r.TransferEncoding) > 0 || r.ContentLength < 0:
		return nil, deny(http.StatusLengthRequired, "a create body must declare its length; chunked bodies are refused")
	case r.ContentLength == 0:
		return nil, deny(http.StatusBadRequest, "a create request needs a body")
	case r.ContentLength > f.cfg.MaxBodyBytes:
		return nil, deny(http.StatusRequestEntityTooLarge, "the create body is larger than "+strconv.FormatInt(f.cfg.MaxBodyBytes, 10)+" bytes")
	}
	ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])
	if !strings.EqualFold(ct, "application/json") {
		return nil, deny(http.StatusBadRequest, "a create body must be JSON")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, f.cfg.MaxBodyBytes+1))
	if err != nil || int64(len(body)) != r.ContentLength {
		return nil, deny(http.StatusBadRequest, "the create body does not match its declared length")
	}
	return body, nil
}

// create checks a create request and forwards the filter's own encoding of
// the body it checked.
func (f *Filter) create(w http.ResponseWriter, r *http.Request, rt route) *denial {
	name := rt.query.Get("name")
	if !strings.HasPrefix(name, f.cfg.NamePrefix) || !containerRef.MatchString(name) || len(name) == len(f.cfg.NamePrefix) {
		return deny(http.StatusForbidden, "a runner container needs a name starting with "+f.cfg.NamePrefix)
	}
	raw, d := f.readBody(r)
	if d != nil {
		return d
	}
	body, err := f.checkCreate(raw)
	if err != nil {
		return deny(http.StatusForbidden, err.Error())
	}
	return f.forward(w, r, http.MethodPost, rt.version, "/containers/create", map[string][]string{"name": {name}}, body)
}

// refuse answers a refused request in the daemon's error shape and logs it.
// The log names the method, the path and the reason, never a body.
func (f *Filter) refuse(w http.ResponseWriter, r *http.Request, d *denial) {
	f.log.Warn("docker request refused", "method", r.Method, "path", truncate(r.URL.EscapedPath(), 200),
		"status", d.status, "reason", d.reason)
	writeJSON(w, d.status, map[string]string{"message": "refused by the sisf docker filter: " + d.reason})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(v) // maps of strings always encode
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
