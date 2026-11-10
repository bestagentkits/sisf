package dockerfilter

import (
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// endpoint is one kind of request the filter lets through.
type endpoint int

const (
	endpointPing endpoint = iota + 1
	endpointInfo
	endpointCreate
	endpointInspect
	endpointStart
	endpointStop
	endpointWait
	endpointLogs
	endpointRemove
)

// route is a request the filter recognised: the endpoint, the API version
// it was addressed to and, for a container endpoint, the container named.
type route struct {
	endpoint  endpoint
	version   string
	container string
	query     url.Values
}

var (
	versionedPath = regexp.MustCompile(`^/v(1\.[0-9]{1,2})(/[A-Za-z0-9_./-]*)$`)
	containerRef  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	containerPath = regexp.MustCompile(`^/containers/([^/]+)/(json|start|stop|wait|logs)$`)
	removePath    = regexp.MustCompile(`^/containers/([^/]+)$`)
)

// queryRule says which parameters an endpoint accepts and checks a value.
type queryRule map[string]func(string) bool

var (
	isBool       = func(v string) bool { return v == "0" || v == "1" || v == "true" || v == "false" }
	isFalse      = func(v string) bool { return v == "0" || v == "false" }
	isSmallCount = func(v string) bool { n, err := strconv.Atoi(v); return err == nil && n >= 0 && n <= 3600 }
	isTail       = func(v string) bool { return v == "all" || isSmallCount(v) }
	isCondition  = func(v string) bool { return v == "not-running" || v == "next-exit" || v == "removed" }
)

// queryRules lists the parameters of each endpoint. An endpoint missing
// here takes none.
var queryRules = map[endpoint]queryRule{
	endpointCreate:  {"name": func(string) bool { return true }}, // checked against the name prefix
	endpointInspect: {"size": isFalse},
	endpointStop:    {"t": isSmallCount},
	endpointWait:    {"condition": isCondition},
	endpointLogs:    {"stdout": isBool, "stderr": isBool, "follow": isBool, "timestamps": isBool, "tail": isTail},
	endpointRemove:  {"force": isBool, "v": isBool},
}

// parseRoute recognises a request or returns why it is refused. It reads
// the path exactly as it was sent: a path with any percent-encoding, a
// backslash, a dot segment or an empty segment is refused rather than
// normalized, so the path the filter judged is the path it forwards.
func (f *Filter) parseRoute(r *http.Request) (route, *denial) {
	switch {
	case r.URL == nil || !strings.HasPrefix(r.RequestURI, "/"):
		return route{}, deny(http.StatusBadRequest, "the request target must be a plain path")
	case strings.ContainsAny(r.RequestURI, "%\\#") || r.URL.RawPath != "" || r.URL.Fragment != "":
		return route{}, deny(http.StatusBadRequest, "encoded or escaped request paths are refused")
	case path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "//"):
		return route{}, deny(http.StatusBadRequest, "the request path must be clean")
	}
	m := versionedPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return route{}, deny(http.StatusForbidden, "only versioned API paths are served")
	}
	rt := route{version: m[1]}
	if !f.versions[rt.version] {
		return route{}, deny(http.StatusForbidden, "API version "+rt.version+" is not allowed")
	}
	rest := m[2]
	switch {
	case rest == "/_ping" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		rt.endpoint = endpointPing
	case rest == "/info" && r.Method == http.MethodGet:
		rt.endpoint = endpointInfo
	case rest == "/containers/create" && r.Method == http.MethodPost:
		rt.endpoint = endpointCreate
	default:
		if d := rt.containerEndpoint(r.Method, rest); d != nil {
			return route{}, d
		}
	}
	query, d := checkQuery(r.URL.RawQuery, queryRules[rt.endpoint])
	if d != nil {
		return route{}, d
	}
	rt.query = query
	return rt, nil
}

// containerEndpoint recognises an action on one container.
func (rt *route) containerEndpoint(method, rest string) *denial {
	var id, action string
	if m := containerPath.FindStringSubmatch(rest); m != nil {
		id, action = m[1], m[2]
	} else if m := removePath.FindStringSubmatch(rest); m != nil && m[1] != "create" && m[1] != "json" {
		id, action = m[1], "remove"
	} else {
		return deny(http.StatusForbidden, "endpoint "+method+" "+rest+" is not allowed")
	}
	want := map[string]struct {
		method   string
		endpoint endpoint
	}{
		"json":   {http.MethodGet, endpointInspect},
		"start":  {http.MethodPost, endpointStart},
		"stop":   {http.MethodPost, endpointStop},
		"wait":   {http.MethodPost, endpointWait},
		"logs":   {http.MethodGet, endpointLogs},
		"remove": {http.MethodDelete, endpointRemove},
	}[action]
	if method != want.method {
		return deny(http.StatusForbidden, "endpoint "+method+" "+rest+" is not allowed")
	}
	if !containerRef.MatchString(id) {
		return deny(http.StatusBadRequest, "the container reference is malformed")
	}
	rt.endpoint, rt.container = want.endpoint, id
	return nil
}

// checkQuery parses a raw query and keeps it only when every parameter is
// known to the endpoint, appears once and has an accepted value.
func checkQuery(raw string, rule queryRule) (url.Values, *denial) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, deny(http.StatusBadRequest, "the query cannot be parsed")
	}
	for key, vs := range values {
		check, known := rule[key]
		switch {
		case !known:
			return nil, deny(http.StatusForbidden, "query parameter "+strconv.Quote(key)+" is not allowed")
		case len(vs) != 1 || !check(vs[0]):
			return nil, deny(http.StatusForbidden, "query parameter "+strconv.Quote(key)+" has a value that is not allowed")
		}
	}
	return values, nil
}
