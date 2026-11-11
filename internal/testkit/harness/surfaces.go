package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
)

// shownState finds the state field of the dashboard's issue page.
var shownState = regexp.MustCompile(`data-field="state">([^<]*)<`)

// SurfaceStates reads the state of one issue twice, as the REST API reports
// it (runs.inspect) and as the dashboard's run page shows it, from the
// control plane at base with a signed-in person's session cookie value.
// Both surfaces call one action service, so the two must agree; a caller
// compares them.
func SurfaceStates(ctx context.Context, base, session, issueID string) (api, dashboard string, err error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cookie := &http.Cookie{Name: "sisf_session", Value: session}

	view, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/auth/session", nil)
	if err != nil {
		return "", "", err
	}
	view.AddCookie(cookie)
	body, err := fetch(client, view, http.StatusOK)
	if err != nil {
		return "", "", fmt.Errorf("session: %w", err)
	}
	var sess struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		return "", "", fmt.Errorf("session answer: %w", err)
	}
	input, _ := json.Marshal(map[string]any{"input": map[string]string{"issue": issueID}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/actions/runs.inspect", bytes.NewReader(input))
	if err != nil {
		return "", "", err
	}
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", sess.CSRF)
	body, err = fetch(client, req, http.StatusOK)
	if err != nil {
		return "", "", fmt.Errorf("API: %w", err)
	}
	var result struct {
		Output struct {
			Record struct {
				State string `json:"state"`
			} `json:"record"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("API answer: %w", err)
	}

	page, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/runs?issue="+url.QueryEscape(issueID), nil)
	if err != nil {
		return "", "", err
	}
	page.AddCookie(cookie)
	body, err = fetch(client, page, http.StatusOK)
	if err != nil {
		return "", "", fmt.Errorf("dashboard: %w", err)
	}
	match := shownState.FindSubmatch(body)
	if match == nil {
		return "", "", fmt.Errorf("the dashboard page shows no state")
	}
	return result.Output.Record.State, html.UnescapeString(string(match[1])), nil
}

// fetch sends req and returns the body of an answer with the wanted status.
func fetch(client *http.Client, req *http.Request, want int) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != want {
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	return body, nil
}

// SurfaceStates reads the state of issue number through both surfaces.
func (f *Factory) SurfaceStates(number int) (api, dashboard string) {
	f.t.Helper()
	f.ownerOnce.Do(func() { f.owner = f.SignInOwner() })
	api, dashboard, err := SurfaceStates(context.Background(), f.controlSrv.URL, f.owner.Session(), f.Issue(number).String())
	if err != nil {
		f.t.Fatalf("harness: %v", err)
	}
	return api, dashboard
}

// Explain asks runs.explain about issue number over HTTP, as the Owner.
func (f *Factory) Explain(number int) runactions.Explanation {
	f.t.Helper()
	var out runactions.Explanation
	f.readRun(runactions.ActionRunsExplain, number, &out)
	return out
}

// Inspect asks runs.inspect about issue number over HTTP, as the Owner.
func (f *Factory) Inspect(number int) runactions.Inspection {
	f.t.Helper()
	var out runactions.Inspection
	f.readRun(runactions.ActionRunsInspect, number, &out)
	return out
}

// readRun calls a run action about issue number and decodes its output.
func (f *Factory) readRun(action string, number int, output any) {
	f.t.Helper()
	f.ownerOnce.Do(func() { f.owner = f.SignInOwner() })
	input, _ := json.Marshal(map[string]any{"input": map[string]string{"issue": f.Issue(number).String()}})
	resp := f.owner.Do(http.MethodPost, "/api/v1/actions/"+action, bytes.NewReader(input),
		http.Header{"Content-Type": {"application/json"}, "X-Csrf-Token": {f.owner.CSRF()}})
	out := struct {
		Output any `json:"output"`
	}{Output: output}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		f.t.Fatalf("harness: %s answered %d: %v", action, resp.StatusCode, err)
	}
}
