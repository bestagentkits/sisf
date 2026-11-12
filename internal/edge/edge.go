// Package edge is the reverse proxy in front of the application. It does
// one thing: send each request to the upstream whose route matches the path,
// or to the default upstream. It terminates no TLS, holds no credentials and
// makes no policy decision; the egress policy of the sandbox is a separate
// concern of its own.
//
// Routes are how a small part of the control plane, such as the webhook
// route, is exposed next to the application while the rest of it stays
// private. A route therefore matches whole path segments of the cleaned
// path, so neither "/reports" nor "/report/../admin" can reach a route
// meant for "/report".
package edge

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"
)

// Route sends the paths below Prefix to Upstream.
type Route struct {
	Prefix   string
	Upstream *url.URL
}

// Config describes what the proxy forwards where.
type Config struct {
	// Default receives every request no route matches. It is required
	// unless Split is set.
	Default *url.URL
	// Split, when set, receives every request no route matches instead of
	// Default: it divides them between a stable and a canary revision and
	// counts the outcome of each. Routes are never split.
	Split *Splitter
	// Routes are matched by the longest prefix. Prefixes must differ.
	Routes []Route
	// Logger receives one line per failed upstream call. It never receives
	// a header, a body or a query. Nil discards them.
	Logger *slog.Logger
}

// upstreamResponseTimeout bounds how long an upstream may take to start answering.
const upstreamResponseTimeout = 60 * time.Second

// ParseUpstream reads a base URL: http or https, a host, and nothing else.
// A path, query or credential in it would be silently dropped or abused when
// requests are rewritten, so it is refused.
func ParseUpstream(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("an upstream must be an http or https URL with a host")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("an upstream is a base URL: scheme, host and port only")
	}
	u.Path = ""
	return u, nil
}

// ParseRoute reads "/prefix=http://host:port".
func ParseRoute(spec string) (Route, error) {
	prefix, target, ok := strings.Cut(spec, "=")
	if !ok {
		return Route{}, errors.New(`a route is written "/prefix=http://host:port"`)
	}
	if err := checkPrefix(prefix); err != nil {
		return Route{}, err
	}
	upstream, err := ParseUpstream(target)
	if err != nil {
		return Route{}, err
	}
	return Route{Prefix: prefix, Upstream: upstream}, nil
}

// checkPrefix accepts an absolute path with no dot segments, query or
// trailing slash. "/" alone is not a route; it is what Default is for.
func checkPrefix(prefix string) error {
	if !strings.HasPrefix(prefix, "/") || prefix == "/" || strings.HasSuffix(prefix, "/") ||
		strings.ContainsAny(prefix, "?#") || path.Clean(prefix) != prefix {
		return fmt.Errorf("route prefix %q must be an absolute path such as /report, with no trailing slash or dot segments", prefix)
	}
	return nil
}

// handler is the proxy: one reverse proxy per upstream, chosen per request.
type handler struct {
	routes   []Route // longest prefix first
	fallback http.Handler
	proxies  map[string]*httputil.ReverseProxy
}

// New validates the configuration and returns the proxy.
func New(cfg Config) (http.Handler, error) {
	if cfg.Default == nil && cfg.Split == nil {
		return nil, errors.New("the edge needs a default upstream")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &handler{proxies: map[string]*httputil.ReverseProxy{}}
	if cfg.Split != nil {
		h.fallback = cfg.Split
	} else {
		h.fallback = newProxy(cfg.Default, log, "")
	}
	seen := map[string]bool{}
	for _, r := range cfg.Routes {
		if err := checkPrefix(r.Prefix); err != nil {
			return nil, err
		}
		if r.Upstream == nil || seen[r.Prefix] {
			return nil, fmt.Errorf("route %q needs an upstream and may be declared once", r.Prefix)
		}
		seen[r.Prefix] = true
		h.routes = append(h.routes, r)
		if h.proxies[r.Upstream.String()] == nil {
			h.proxies[r.Upstream.String()] = newProxy(r.Upstream, log, "")
		}
	}
	// Longest prefix first, so a specific route wins over a general one.
	for i := 1; i < len(h.routes); i++ {
		for j := i; j > 0 && len(h.routes[j].Prefix) > len(h.routes[j-1].Prefix); j-- {
			h.routes[j], h.routes[j-1] = h.routes[j-1], h.routes[j]
		}
	}
	return h, nil
}

// newProxy forwards to one upstream. Rewrite starts from an empty outbound
// request, so a client-supplied X-Forwarded-* header is not passed on; the
// edge states who connected to it. A non-empty revision is stamped on every
// response, an upstream's own header of that name included, so the label
// always says which revision the edge chose.
func newProxy(target *url.URL, log *slog.Logger, revision string) *httputil.ReverseProxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamResponseTimeout
	return &httputil.ReverseProxy{
		ModifyResponse: func(resp *http.Response) error {
			if revision != "" {
				resp.Header.Set(RevisionHeader, revision)
			}
			return nil
		},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Warn("upstream call failed", "upstream", target.Host, "error", err.Error())
			if revision != "" {
				w.Header().Set(RevisionHeader, revision)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream unavailable"}` + "\n"))
		},
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Decide on the cleaned path and forward that same path, so the upstream
	// never sees a dot segment that it could resolve into another route.
	clean := path.Clean("/" + r.URL.Path)
	r.URL.Path, r.URL.RawPath = clean, ""
	for _, route := range h.routes {
		if clean == route.Prefix || strings.HasPrefix(clean, route.Prefix+"/") {
			h.proxies[route.Upstream.String()].ServeHTTP(w, r)
			return
		}
	}
	h.fallback.ServeHTTP(w, r)
}
