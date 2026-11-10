package compose

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// apiTimeout bounds one call to the Docker API.
const apiTimeout = 30 * time.Second

// dockerAPI speaks the few Docker endpoints a release needs, to the
// endpoint the configuration names and nothing else.
type dockerAPI struct {
	base    string
	version string
	http    *http.Client
}

// newDockerAPI reads the endpoint: "unix:///path/to.sock" or "http://host:port".
// There is no default and no lookup in the environment, so a backend that was
// not handed an endpoint cannot reach a Docker daemon at all.
func newDockerAPI(endpoint, version string) (*dockerAPI, error) {
	u, err := url.Parse(endpoint)
	if err != nil || endpoint == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the Docker endpoint must be unix:///path or http://host:port, and is set by configuration")
	}
	api := &dockerAPI{version: version, http: &http.Client{Timeout: apiTimeout}}
	switch {
	case u.Scheme == "unix" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
		socket := u.Path
		api.base = "http://docker"
		api.http.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
	case u.Scheme == "http" && u.Hostname() != "" && (u.Path == "" || u.Path == "/") && u.User == nil:
		api.base = "http://" + u.Host
	default:
		return nil, core.NewError(core.CodeInvalidArgument, "the Docker endpoint must be unix:///path or http://host:port")
	}
	return api, nil
}

// call sends one request and returns the response when its status is one of
// want. Anything else is a typed error: a refusal is PERMISSION_DENIED, a
// missing container NOT_FOUND, a name clash CONFLICT, the rest UNAVAILABLE.
func (a *dockerAPI) call(ctx context.Context, method, path string, query url.Values, body any, want ...int) (*http.Response, error) {
	target := a.base + "/v" + a.version + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, core.NewError(core.CodeInternal, "a Docker request could not be encoded").WithCause(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "a Docker request could not be built").WithCause(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, core.NewError(core.CodeUnavailable, "the Docker endpoint did not answer").WithCause(err)
	}
	for _, status := range want {
		if resp.StatusCode == status {
			return resp, nil
		}
	}
	defer func() { _ = resp.Body.Close() }()
	var answer struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer) // the status alone is enough
	message := fmt.Sprintf("%s %s answered %d: %.300s", method, path, resp.StatusCode, answer.Message)
	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, core.NewError(core.CodePermissionDenied, message)
	case http.StatusNotFound:
		return nil, core.NewError(core.CodeNotFound, message)
	case http.StatusConflict:
		return nil, core.NewError(core.CodeConflict, message)
	}
	return nil, core.NewError(core.CodeUnavailable, message)
}

func (a *dockerAPI) decode(ctx context.Context, method, path string, query url.Values, body, out any, want ...int) error {
	resp, err := a.call(ctx, method, path, query, body, want...)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return core.NewError(core.CodeUnavailable, method+" "+path+" answered with something unreadable").WithCause(err)
	}
	return nil
}

// containerView is what the backend reads back about a container.
type containerView struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

func (a *dockerAPI) create(ctx context.Context, name string, spec createSpec) error {
	return a.decode(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, spec, nil, http.StatusCreated, http.StatusOK)
}

func (a *dockerAPI) inspect(ctx context.Context, name string) (containerView, error) {
	var v containerView
	err := a.decode(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil, &v, http.StatusOK)
	return v, err
}

func (a *dockerAPI) start(ctx context.Context, name string) error {
	return a.decode(ctx, http.MethodPost, "/containers/"+name+"/start", nil, nil, nil, http.StatusNoContent, http.StatusNotModified)
}

func (a *dockerAPI) stop(ctx context.Context, name string) error {
	err := a.decode(ctx, http.MethodPost, "/containers/"+name+"/stop", url.Values{"t": {"5"}}, nil, nil, http.StatusNoContent, http.StatusNotModified)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

// remove deletes the container and never its volumes: a release replaces
// code and configuration only.
func (a *dockerAPI) remove(ctx context.Context, name string) error {
	err := a.decode(ctx, http.MethodDelete, "/containers/"+name, url.Values{"force": {"1"}}, nil, nil, http.StatusNoContent, http.StatusOK)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

// list returns the containers, running or not, that carry all the labels.
func (a *dockerAPI) list(ctx context.Context, labels ...string) ([]containerView, error) {
	filters, err := json.Marshal(map[string][]string{"label": labels})
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "a Docker filter could not be encoded").WithCause(err)
	}
	var out []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := a.decode(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	views := make([]containerView, 0, len(out))
	for _, c := range out {
		var v containerView
		if len(c.Names) > 0 {
			v.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		v.Config.Labels, v.State.Running = c.Labels, c.State == "running"
		views = append(views, v)
	}
	return views, nil
}

// logsSince returns the container's output since t, both streams joined,
// bounded to a few megabytes.
func (a *dockerAPI) logsSince(ctx context.Context, name string, since time.Time) (string, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "since": {fmt.Sprint(since.Unix())}}
	resp, err := a.call(ctx, http.MethodGet, "/containers/"+name+"/logs", q, nil, http.StatusOK)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	body := io.LimitReader(resp.Body, 4<<20)
	if !strings.Contains(resp.Header.Get("Content-Type"), "multiplexed") {
		_, err = io.Copy(&out, body)
		return out.String(), err
	}
	err = demux(body, &out)
	return out.String(), err
}

// demux joins the frames of the daemon's multiplexed stream: an eight-byte
// header (stream, three zero bytes, big-endian length) and the payload.
func demux(r io.Reader, dst io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil // the bound cut the stream or it ended; what was read is what counts
			}
			return core.NewError(core.CodeUnavailable, "the container's output stream was cut").WithCause(err)
		}
		if _, err := io.CopyN(dst, r, int64(binary.BigEndian.Uint32(header[4:]))); err != nil {
			return nil // a truncated last frame
		}
	}
}
