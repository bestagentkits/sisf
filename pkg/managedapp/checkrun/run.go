// Package checkrun runs a managed application's checks against a revision.
//
// A result holds only facts the runner measured itself: the check, the step,
// the expected and observed status, the latency and a fixed reason. It never
// carries the response body or headers, which the application's visitors can
// influence, so a result is safe to put in an issue a coding agent reads.
package checkrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// maxBody bounds what is read of one response.
const maxBody = 4 << 20

// Reason says why a step failed, from a fixed set.
type Reason string

const (
	ReasonUnreachable Reason = "unreachable"       // no response
	ReasonStatus      Reason = "unexpected_status" // a status other than expected
	ReasonSlow        Reason = "too_slow"          // over the check's latency bound
	ReasonContent     Reason = "missing_content"   // expected text absent
	ReasonValue       Reason = "unexpected_value"  // a JSON value differs or is absent
	ReasonCapture     Reason = "nothing_to_capture"
)

// Result is the outcome of one check.
type Result struct {
	Check  string `json:"check"`
	Passed bool   `json:"passed"`
	// Step is the index of the failing step; -1 when the check passed.
	Step           int    `json:"step"`
	Reason         Reason `json:"reason,omitempty"`
	ExpectedStatus int    `json:"expected_status,omitempty"`
	ObservedStatus int    `json:"observed_status,omitempty"`
	// LatencyMS is the slowest step's latency; BoundMS the check's bound.
	LatencyMS int64 `json:"latency_ms"`
	BoundMS   int   `json:"bound_ms"`
	// Field names the JSON path or capture that failed, from the Spec.
	Field string `json:"field,omitempty"`
}

// Runner runs checks against one base URL.
type Runner struct {
	BaseURL string
	// Client is used for every request; its Jar is replaced per check.
	Client *http.Client
	// Now measures latency; time.Now when nil.
	Now func() time.Time
}

// Run runs c and returns its result. It never returns an error: a failure
// to reach the revision is a failed check.
func (r Runner) Run(ctx context.Context, c managedapp.Check) Result {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	client := *r.Client
	client.Jar, _ = cookiejar.New(nil)
	vars := map[string]string{}
	res := Result{Check: c.Name, Step: -1, BoundMS: c.Latency()}
	for i, st := range c.Steps {
		want := st.Status
		if want == 0 {
			want = http.StatusOK
		}
		fail := func(reason Reason, observed int, field string) Result {
			res.Step, res.Reason, res.ExpectedStatus, res.ObservedStatus, res.Field = i, reason, want, observed, field
			return res
		}
		req, err := r.request(ctx, st, vars)
		if err != nil {
			return fail(ReasonUnreachable, 0, "")
		}
		start := now()
		resp, err := client.Do(req)
		if err != nil {
			return fail(ReasonUnreachable, 0, "")
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		latency := now().Sub(start).Milliseconds()
		if latency > res.LatencyMS {
			res.LatencyMS = latency
		}
		switch {
		case resp.StatusCode != want:
			return fail(ReasonStatus, resp.StatusCode, "")
		case latency > int64(c.Latency()):
			return fail(ReasonSlow, resp.StatusCode, "")
		case st.Contains != "" && !bytes.Contains(body, []byte(st.Contains)):
			return fail(ReasonContent, resp.StatusCode, "")
		}
		var doc any
		if len(st.JSON) > 0 || len(st.CaptureJSON) > 0 {
			_ = json.Unmarshal(body, &doc)
		}
		for path, expected := range st.JSON {
			if got, ok := lookup(doc, path); !ok || got != expected {
				return fail(ReasonValue, resp.StatusCode, path)
			}
		}
		for name, path := range st.CaptureJSON {
			got, ok := lookup(doc, path)
			if !ok {
				return fail(ReasonCapture, resp.StatusCode, path)
			}
			vars[name] = got
		}
		for name, header := range st.CaptureHeader {
			got := resp.Header.Get(header)
			if got == "" {
				return fail(ReasonCapture, resp.StatusCode, header)
			}
			vars[name] = got
		}
	}
	res.Passed = true
	return res
}

func (r Runner) request(ctx context.Context, st managedapp.Step, vars map[string]string) (*http.Request, error) {
	method := strings.ToUpper(st.Method)
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if st.Body != "" {
		body = strings.NewReader(expand(st.Body, vars))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.BaseURL, "/")+expand(st.Path, vars), body)
	if err != nil {
		return nil, err
	}
	if st.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "sisf-check")
	for k, v := range st.Headers {
		req.Header.Set(k, expand(v, vars))
	}
	return req, nil
}

// expand replaces ${name} with captured values.
func expand(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// lookup follows a dotted path; a number indexes an array. Scalars are
// returned in their JSON text form without quotes.
func lookup(doc any, path string) (string, bool) {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return "", false
			}
			cur = v[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case nil:
		return "", false
	default:
		return fmt.Sprint(v), true
	}
}
