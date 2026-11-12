// Package healthprobe reads what a running application says about itself at
// /healthz: whether it is healthy and which revision it was started as.
// Deployment backends use it as one of the two independent sources an
// observed revision is read from, so what a deploy call returned is never
// the evidence.
package healthprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxBody bounds an answer; a health check is a few dozen bytes.
const maxBody = 16 << 10

// Health is one answer of the health check.
type Health struct {
	// Healthy is true for a 200 answer whose status is "ok".
	Healthy bool
	// Revision is what the instance says it runs, empty if it says nothing.
	// It is read from a 503 answer as well, so an unhealthy instance can
	// still be identified.
	Revision string
}

// Probe calls GET baseURL/healthz. An instance that cannot be reached or
// does not answer with the expected JSON is UNAVAILABLE: nothing may be
// concluded about it.
func Probe(ctx context.Context, client *http.Client, baseURL string) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
	if err != nil {
		return Health{}, core.NewError(core.CodeInvalidArgument, "the health check address is not a URL").WithCause(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Health{}, core.NewError(core.CodeUnavailable, "the application did not answer its health check").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status   string `json:"status"`
		Revision string `json:"revision"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return Health{}, core.NewError(core.CodeUnavailable, "the application's health check was not readable").WithCause(err)
	}
	return Health{Healthy: resp.StatusCode == http.StatusOK && body.Status == "ok", Revision: body.Revision}, nil
}
