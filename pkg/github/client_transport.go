package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

const maxResponseBytes = 32 << 20

// call performs a request against a path below the API root.
func (c *RESTClient) call(ctx context.Context, method, path string, query url.Values, body any, header http.Header) (apiResponse, error) {
	raw := c.base.String() + path
	if len(query) > 0 {
		raw += "?" + query.Encode()
	}
	return c.callURL(ctx, method, raw, body, header)
}

// callURL performs a request to an absolute URL on the API host. It sends the
// token only to that host: a URL anywhere else is refused.
func (c *RESTClient) callURL(ctx context.Context, method, rawURL string, body any, header http.Header) (apiResponse, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme != c.base.Scheme || target.Host != c.base.Host {
		return apiResponse{}, core.NewError(core.CodeUnavailable, "refusing to send the GitHub token to a host other than the API host")
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return apiResponse{}, core.NewError(core.CodeInvalidArgument, "the request body could not be encoded").WithCause(err)
		}
	}
	idempotent := method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete || method == http.MethodPatch
	for attempt := 1; ; attempt++ {
		resp, sendErr := c.send(ctx, method, rawURL, payload, header)
		last := attempt >= c.cfg.MaxAttempts
		switch {
		case sendErr != nil && (ctx.Err() != nil || !isRetryable(sendErr)):
			return apiResponse{}, sendErr
		case sendErr != nil:
			if !idempotent || last {
				return apiResponse{}, sendErr
			}
			if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
				return apiResponse{}, err
			}
			continue
		}
		if limited, wait := c.rateLimited(resp); limited {
			if last || wait > c.cfg.MaxWait || outlastsDeadline(ctx, wait) {
				return apiResponse{}, rateLimitError(method, target.Path, wait, last && wait <= c.cfg.MaxWait)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return apiResponse{}, err
			}
			continue
		}
		if resp.status >= 500 {
			// A request that may have been applied is not repeated unless it
			// is safe to: a second POST could create a second comment.
			if !idempotent || last {
				return apiResponse{}, unavailable(method, target.Path, resp.status)
			}
			if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
				return apiResponse{}, err
			}
			continue
		}
		if resp.status >= 300 && resp.status != http.StatusNotModified {
			return apiResponse{}, statusError(method, target.Path, resp)
		}
		return resp, nil
	}
}

// outlastsDeadline reports whether waiting wait would run past ctx's
// deadline: the caller then learns of the rate limit GitHub answered with,
// not of a timeout that hides it. A context's deadline is wall-clock time,
// so it is compared with the wall clock, not the client's clock.
func outlastsDeadline(ctx context.Context, wait time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) < wait
}

type retryableError struct{ error }

func isRetryable(err error) bool { var r retryableError; return errors.As(err, &r) }

func unavailable(method, path string, status int) error {
	return core.NewError(core.CodeUnavailable, "GitHub is unavailable for "+method+" "+path+" (status "+itoa(status)+")")
}

// send makes one HTTP exchange.
func (c *RESTClient) send(ctx context.Context, method, rawURL string, payload []byte, header http.Header) (apiResponse, error) {
	token, err := c.token(ctx)
	if err != nil {
		return apiResponse{}, core.NewError(core.CodeUnauthenticated, "the installation token could not be obtained").WithCause(err)
	}
	if token.IsZero() {
		return apiResponse{}, core.NewError(core.CodeUnauthenticated, "the installation token is empty")
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return apiResponse{}, core.NewError(core.CodeInvalidArgument, "the request could not be built").WithCause(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+string(token.Reveal()))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "sisf-factory")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return apiResponse{}, ctxError(ctxErr)
		}
		// The transport error can echo the URL but never the token header.
		return apiResponse{}, retryableError{core.NewError(core.CodeUnavailable, "GitHub could not be reached").WithCause(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return apiResponse{}, retryableError{core.NewError(core.CodeUnavailable, "the GitHub response could not be read").WithCause(err)}
	}
	return apiResponse{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func ctxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewError(core.CodeTimeout, "the GitHub request timed out").WithCause(err)
	}
	return core.NewError(core.CodeUnavailable, "the GitHub request was cancelled").WithCause(err)
}

// backoff is the wait after the given failed attempt: it doubles each time.
func (c *RESTClient) backoff(attempt int) time.Duration {
	return c.cfg.ServerErrorBackoff << (attempt - 1)
}

// sleep waits on the injected clock, or until the context ends.
func (c *RESTClient) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-c.clock.After(d):
		return nil
	case <-ctx.Done():
		return ctxError(ctx.Err())
	}
}

// decode parses a response body.
func decode(resp apiResponse, v any) error {
	if err := json.Unmarshal(resp.body, v); err != nil {
		return core.NewError(core.CodeUnavailable, "GitHub returned a response that could not be read").WithCause(err)
	}
	return nil
}
