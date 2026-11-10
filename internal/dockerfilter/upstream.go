package dockerfilter

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
)

// maxInspectBytes bounds an inspect answer the filter reads itself.
const maxInspectBytes = 1 << 20

// upstreamRequest builds a request to the daemon from parts the filter
// checked. Nothing of the client's request is copied but the context.
func (f *Filter) upstreamRequest(r *http.Request, method, version, path string, query url.Values, body []byte) (*http.Request, error) {
	target := f.cfg.UpstreamURL + "/v" + version + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(body))
	}
	return req, nil
}

// forward sends a checked request to the daemon and streams the answer
// back, flushing as it goes so that wait and logs arrive when they happen.
func (f *Filter) forward(w http.ResponseWriter, r *http.Request, method, version, path string, query url.Values, body []byte) *denial {
	req, err := f.upstreamRequest(r, method, version, path, query, body)
	if err != nil {
		return deny(http.StatusInternalServerError, "the request to the daemon could not be built")
	}
	resp, err := f.cfg.Upstream.RoundTrip(req)
	if err != nil {
		return deny(http.StatusBadGateway, "the Docker daemon did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range []string{"Content-Type", "Api-Version", "Docker-Experimental", "Ostype"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)
	if method == http.MethodHead {
		return nil
	}
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return nil
			}
			_ = flusher.Flush() // a writer that cannot flush still delivers at the end
		}
		if readErr != nil {
			return nil
		}
	}
}

// fetch makes a request of its own to the daemon and reads the answer.
func (f *Filter) fetch(r *http.Request, version, path string) (int, []byte, *denial) {
	req, err := f.upstreamRequest(r, http.MethodGet, version, path, nil, nil)
	if err != nil {
		return 0, nil, deny(http.StatusInternalServerError, "the request to the daemon could not be built")
	}
	resp, err := f.cfg.Upstream.RoundTrip(req)
	if err != nil {
		return 0, nil, deny(http.StatusBadGateway, "the Docker daemon did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInspectBytes+1))
	if err != nil || len(body) > maxInspectBytes {
		return 0, nil, deny(http.StatusBadGateway, "the daemon's answer could not be read")
	}
	return resp.StatusCode, body, nil
}

// containerAction acts on one container, but only on a runner container:
// the filter inspects it first and then addresses the full ID it read.
// Container IDs and labels never change, so the container checked is the
// container acted on.
func (f *Filter) containerAction(w http.ResponseWriter, r *http.Request, rt route) *denial {
	status, body, d := f.fetch(r, rt.version, "/containers/"+rt.container+"/json")
	switch {
	case d != nil:
		return d
	case status == http.StatusNotFound:
		return deny(http.StatusNotFound, "no such container")
	case status != http.StatusOK:
		return deny(http.StatusBadGateway, "the daemon could not inspect the container")
	}
	var seen struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(body, &seen); err != nil || !fullID.MatchString(seen.ID) {
		return deny(http.StatusBadGateway, "the daemon's inspect answer is not understood")
	}
	if f.cfg.Role == RoleDeploy && !f.deployed(seen.Config.Labels) {
		return deny(http.StatusForbidden, "the container is not a deployed unit of this factory")
	}
	if f.cfg.Role != RoleDeploy && seen.Config.Labels[f.cfg.RunnerLabel] != string(f.cfg.RunnerValue) {
		return deny(http.StatusForbidden, "the container is not a runner of this factory")
	}
	if rt.endpoint == endpointInspect {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return nil
	}
	action := containerActions[rt.endpoint]
	return f.forward(w, r, action.method, rt.version, "/containers/"+seen.ID+action.suffix, rt.query, nil)
}

// fullID is a container ID as the daemon reports it.
var fullID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// containerActions maps a container endpoint to the daemon request it makes.
var containerActions = map[endpoint]struct{ method, suffix string }{
	endpointStart:  {http.MethodPost, "/start"},
	endpointStop:   {http.MethodPost, "/stop"},
	endpointWait:   {http.MethodPost, "/wait"},
	endpointLogs:   {http.MethodGet, "/logs"},
	endpointRemove: {http.MethodDelete, ""},
}

// info answers with the names of the allowed runtimes the daemon has and
// nothing else the daemon reports about itself.
func (f *Filter) info(w http.ResponseWriter, r *http.Request, rt route) *denial {
	status, body, d := f.fetch(r, rt.version, "/info")
	switch {
	case d != nil:
		return d
	case status != http.StatusOK:
		return deny(http.StatusBadGateway, "the daemon could not report its runtimes")
	}
	var seen struct {
		Runtimes       map[string]json.RawMessage `json:"Runtimes"`
		DefaultRuntime string                     `json:"DefaultRuntime"`
	}
	if err := json.Unmarshal(body, &seen); err != nil {
		return deny(http.StatusBadGateway, "the daemon's info answer is not understood")
	}
	names := make([]string, 0, len(seen.Runtimes))
	for name := range seen.Runtimes {
		if name != "" && f.runtimes[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	runtimes := make(map[string]struct{}, len(names))
	for _, name := range names {
		runtimes[name] = struct{}{}
	}
	answer := map[string]any{"Runtimes": runtimes}
	if f.runtimes[seen.DefaultRuntime] && seen.DefaultRuntime != "" {
		answer["DefaultRuntime"] = seen.DefaultRuntime
	}
	writeJSON(w, http.StatusOK, answer)
	return nil
}
