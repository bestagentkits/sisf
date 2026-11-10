package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/core"
)

// client speaks the Docker API through the request filter. It never talks
// to a daemon socket itself.
type client struct {
	base    string
	version string
	http    *http.Client
}

// call sends one request and returns the response for a wanted status, or
// a typed error: a refusal by the filter is PERMISSION_DENIED, a missing
// container NOT_FOUND, anything else UNAVAILABLE.
func (c *client) call(ctx context.Context, method, path string, query url.Values, body any, want ...int) (*http.Response, error) {
	target := c.base + "/v" + c.version + path
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
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, core.NewError(core.CodeUnavailable, "the Docker filter did not answer").WithCause(err)
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
	message := fmt.Sprintf("%s %s answered %d: %s", method, path, resp.StatusCode, truncate(answer.Message, 300))
	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, core.NewError(core.CodePermissionDenied, message)
	case http.StatusNotFound:
		return nil, core.NewError(core.CodeNotFound, message)
	}
	return nil, core.NewError(core.CodeUnavailable, message)
}

func (c *client) decode(ctx context.Context, method, path string, query url.Values, body, out any, want ...int) error {
	resp, err := c.call(ctx, method, path, query, body, want...)
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

func (c *client) ping(ctx context.Context) error {
	return c.decode(ctx, http.MethodGet, "/_ping", nil, nil, nil, http.StatusOK)
}

// runtimes returns the container runtimes the filter reports.
func (c *client) runtimes(ctx context.Context) (map[string]json.RawMessage, error) {
	var info struct {
		Runtimes map[string]json.RawMessage `json:"Runtimes"`
	}
	err := c.decode(ctx, http.MethodGet, "/info", nil, nil, &info, http.StatusOK)
	return info.Runtimes, err
}

// network reads the sandbox network as the filter reports it.
func (c *client) network(ctx context.Context, name string) (dockerfilter.NetworkView, error) {
	var view dockerfilter.NetworkView
	err := c.decode(ctx, http.MethodGet, "/networks/"+name, nil, nil, &view, http.StatusOK)
	return view, err
}

func (c *client) create(ctx context.Context, name string, req dockerfilter.CreateRequest) (string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	err := c.decode(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, req, &created, http.StatusCreated, http.StatusOK)
	if err == nil && created.ID == "" {
		err = core.NewError(core.CodeUnavailable, "the daemon created a container without an ID")
	}
	return created.ID, err
}

// containerView is what the adapter reads back about a container.
type containerView struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string `json:"Labels"`
		Image  string            `json:"Image"`
		User   string            `json:"User"`
	} `json:"Config"`
	HostConfig map[string]json.RawMessage `json:"HostConfig"`
	State      struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	} `json:"State"`
}

func (c *client) inspect(ctx context.Context, id string) (containerView, error) {
	var view containerView
	err := c.decode(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &view, http.StatusOK)
	return view, err
}

func (c *client) start(ctx context.Context, id string) error {
	return c.decode(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil, http.StatusNoContent, http.StatusNotModified)
}

func (c *client) wait(ctx context.Context, id string) (int, error) {
	var result struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	err := c.decode(ctx, http.MethodPost, "/containers/"+id+"/wait", url.Values{"condition": {"not-running"}}, nil, &result, http.StatusOK)
	if err == nil && result.Error != nil && result.Error.Message != "" {
		err = core.NewError(core.CodeUnavailable, "waiting for the container failed: "+truncate(result.Error.Message, 300))
	}
	return result.StatusCode, err
}

func (c *client) stop(ctx context.Context, id string) error {
	err := c.decode(ctx, http.MethodPost, "/containers/"+id+"/stop", url.Values{"t": {"0"}}, nil, nil, http.StatusNoContent, http.StatusNotModified)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

func (c *client) remove(ctx context.Context, id string) error {
	err := c.decode(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil, http.StatusNoContent, http.StatusOK)
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	return err
}

// logs copies the container's output, separated into its two streams, into
// stdout and stderr.
func (c *client) logs(ctx context.Context, id string, stdout, stderr io.Writer) error {
	resp, err := c.call(ctx, http.MethodGet, "/containers/"+id+"/logs", url.Values{"stdout": {"1"}, "stderr": {"1"}}, nil, http.StatusOK)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if !strings.Contains(resp.Header.Get("Content-Type"), "multiplexed") {
		_, err = io.Copy(stdout, resp.Body) // a raw stream carries one stream only
		return err
	}
	return demux(resp.Body, stdout, stderr)
}

// demux splits the daemon's multiplexed stream: frames of an eight-byte
// header (stream, three zero bytes, big-endian length) and the payload.
func demux(r io.Reader, stdout, stderr io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return core.NewError(core.CodeUnavailable, "the container's output stream was cut").WithCause(err)
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		dst := stdout
		if header[0] == 2 {
			dst = stderr
		}
		if _, err := io.CopyN(dst, r, size); err != nil {
			return core.NewError(core.CodeUnavailable, "the container's output stream was cut").WithCause(err)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
