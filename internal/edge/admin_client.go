package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Controller is what a deployment backend needs of the edge: to read and
// change the division of traffic and to read what each revision served.
// *Splitter satisfies it in process and *AdminClient across processes.
type Controller interface {
	Split(ctx context.Context) (Split, error)
	SetSplit(ctx context.Context, split Split) error
	// Counts returns the counts per revision over the last window, or the
	// totals since the edge started when window is zero.
	Counts(ctx context.Context, window time.Duration) (map[string]Counts, error)
}

// AdminClient drives an admin handler over HTTP.
type AdminClient struct {
	base   string
	token  string
	client *http.Client
}

// NewAdminClient returns a client for the admin handler at base, a URL as
// ParseUpstream reads it. A nil client gets a ten second timeout.
func NewAdminClient(base, token string, client *http.Client) (*AdminClient, error) {
	u, err := ParseUpstream(base)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "edge admin address: "+err.Error())
	}
	if token == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the edge admin client needs a token")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &AdminClient{base: u.String(), token: token, client: client}, nil
}

func (c *AdminClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return core.NewError(core.CodeInternal, "the edge request could not be encoded").WithCause(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return core.NewError(core.CodeInternal, "the edge request could not be built").WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the edge did not answer").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAdminBody))
		code := core.CodeUnavailable
		switch resp.StatusCode {
		case http.StatusBadRequest:
			code = core.CodeInvalidArgument
		case http.StatusUnauthorized:
			code = core.CodeUnauthenticated
		}
		return core.NewError(code, method+" "+path+" on the edge answered "+http.StatusText(resp.StatusCode))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return core.NewError(core.CodeUnavailable, "the edge answered with something unreadable").WithCause(err)
	}
	return nil
}

// Split returns the division in force.
func (c *AdminClient) Split(ctx context.Context) (Split, error) {
	var s Split
	err := c.do(ctx, http.MethodGet, "/split", nil, &s)
	return s, err
}

// SetSplit replaces the division.
func (c *AdminClient) SetSplit(ctx context.Context, split Split) error {
	var applied Split
	return c.do(ctx, http.MethodPut, "/split", split, &applied)
}

// Counts reads the per-revision counts.
func (c *AdminClient) Counts(ctx context.Context, window time.Duration) (map[string]Counts, error) {
	path := "/counters"
	if window != 0 {
		path += "?window=" + url.QueryEscape(window.String())
	}
	var answer CountsAnswer
	if err := c.do(ctx, http.MethodGet, path, nil, &answer); err != nil {
		return nil, err
	}
	return answer.Revisions, nil
}
