// Package synthetic is the synthetic check suite of the sample app. A
// release monitor runs it again and again against a candidate while real
// traffic is too thin to judge it: the candidate passes the hold only when
// every run passes. It covers the app's flows: the page and its durable
// visit count, the health check with the revision, the metrics, and the
// refusal of writes.
package synthetic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxBody bounds what a check reads from the app.
const maxBody = 64 << 10

// Target is the instance under test.
type Target struct {
	// BaseURL is the scheme, host and port of the instance, with no path.
	BaseURL string
	// ExpectedRevision is the revision the instance must report; without it
	// the health check cannot say which version it is looking at.
	ExpectedRevision string
	// Client is used for every request; a client with a ten second timeout
	// when nil.
	Client *http.Client
}

// Result is the outcome of one check.
type Result struct {
	Name string
	// Err is nil when the check passed.
	Err error
}

// Report is the outcome of one run of the suite.
type Report struct{ Results []Result }

// Passed reports whether every check passed. An empty report did not pass.
func (r Report) Passed() bool {
	for _, res := range r.Results {
		if res.Err != nil {
			return false
		}
	}
	return len(r.Results) > 0
}

// Failures returns the checks that failed.
func (r Report) Failures() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Err != nil {
			out = append(out, res)
		}
	}
	return out
}

// Run executes every check once. It never returns early: a monitor wants the
// whole picture of a bad candidate, and a target that is not a usable URL
// fails every check.
func Run(ctx context.Context, t Target) Report {
	r := &runner{target: t}
	if r.target.Client == nil {
		r.target.Client = &http.Client{Timeout: 10 * time.Second}
	}
	checks := []struct {
		name string
		run  func(context.Context) error
	}{
		{"health reports the expected revision", r.health},
		{"page counts visits", r.page},
		{"metrics are exposed", r.metrics},
		{"writes are refused", r.writesRefused},
	}
	base, err := url.Parse(t.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		err = errors.New("the target needs an http or https base URL")
	}
	var report Report
	for _, c := range checks {
		res := Result{Name: c.name, Err: err}
		if err == nil {
			res.Err = c.run(ctx)
		}
		report.Results = append(report.Results, res)
	}
	return report
}

type runner struct{ target Target }

// do sends one request and returns the status and a bounded body.
func (r *runner) do(ctx context.Context, method, path string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.target.BaseURL, "/")+path, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := r.target.Client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, string(body), err
}

func (r *runner) health(ctx context.Context) error {
	if r.target.ExpectedRevision == "" {
		return errors.New("no expected revision was given")
	}
	code, body, err := r.do(ctx, http.MethodGet, "/healthz")
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("GET /healthz answered %d", code)
	}
	var h struct{ Status, Revision string }
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		return errors.New("GET /healthz did not answer with JSON")
	}
	if h.Status != "ok" {
		return fmt.Errorf("health status is %q", h.Status)
	}
	if h.Revision != r.target.ExpectedRevision {
		return fmt.Errorf("the instance reports revision %q, want %q", h.Revision, r.target.ExpectedRevision)
	}
	return nil
}

var visitPattern = regexp.MustCompile(`visit (\d+)`)

// visit loads the page and returns the visit number it shows.
func (r *runner) visit(ctx context.Context) (int64, error) {
	code, body, err := r.do(ctx, http.MethodGet, "/")
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, fmt.Errorf("GET / answered %d", code)
	}
	m := visitPattern.FindStringSubmatch(body)
	if m == nil {
		return 0, errors.New("the page does not show a visit number")
	}
	return strconv.ParseInt(m[1], 10, 64)
}

func (r *runner) page(ctx context.Context) error {
	first, err := r.visit(ctx)
	if err != nil {
		return err
	}
	second, err := r.visit(ctx)
	if err != nil {
		return err
	}
	if second <= first {
		return fmt.Errorf("the visit count went from %d to %d instead of up", first, second)
	}
	return nil
}

func (r *runner) metrics(ctx context.Context) error {
	code, body, err := r.do(ctx, http.MethodGet, "/metrics")
	if err != nil {
		return err
	}
	if code != http.StatusOK || !strings.Contains(body, "hello_up 1") {
		return fmt.Errorf("GET /metrics answered %d without hello_up 1", code)
	}
	return nil
}

func (r *runner) writesRefused(ctx context.Context) error {
	code, _, err := r.do(ctx, http.MethodPost, "/")
	if err != nil {
		return err
	}
	if code != http.StatusMethodNotAllowed {
		return fmt.Errorf("POST / answered %d, want 405", code)
	}
	return nil
}
