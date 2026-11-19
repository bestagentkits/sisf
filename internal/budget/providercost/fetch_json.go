package providercost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxResponse bounds a provider's answer; a cost report is a few rows.
const maxResponse = 4 << 20

// maxPages bounds how many pages one read follows.
const maxPages = 50

// defaultTimeout bounds one request when the caller gives no client.
const defaultTimeout = 30 * time.Second

// errExpiredCursor is a page cursor the provider no longer accepts; the
// read starts again from the first page.
var errExpiredCursor = core.NewError(core.CodeConflict, "the provider's page cursor expired")

// getJSON sends req and decodes a successful answer into out. The
// provider's answer is data: only its status and the fields out names are
// read, and an error never quotes its body, which may echo the request.
func getJSON(ctx context.Context, client *http.Client, req *http.Request, provider string, out any) error {
	c := http.Client{Timeout: defaultTimeout}
	if client != nil {
		c = *client
	}
	// A redirect would carry the admin key, sent in a header of the
	// provider's own, to wherever it points; none is followed.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return core.NewError(core.CodeUnavailable, provider+" did not answer the cost report").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	switch {
	case err != nil:
		return core.NewError(core.CodeUnavailable, provider+"'s cost report could not be read").WithCause(err)
	case len(body) > maxResponse:
		return core.NewError(core.CodeUnavailable, provider+"'s cost report is larger than expected")
	}
	status := fmt.Sprintf("%s answered %d to the cost report", provider, resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusGone:
		return errExpiredCursor
	case resp.StatusCode == http.StatusUnauthorized:
		return core.NewError(core.CodeUnauthenticated, status+"; check the admin key")
	case resp.StatusCode == http.StatusForbidden:
		return core.NewError(core.CodePermissionDenied, status+"; the key may not read costs")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return core.NewError(core.CodeUnavailable, status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return core.NewError(core.CodeUnavailable, provider+"'s cost report is not in the expected form").WithCause(err)
	}
	return nil
}
