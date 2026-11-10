package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// shownState finds the state field of the dashboard's issue page.
var shownState = regexp.MustCompile(`data-field="state">([^<]*)<`)

// SurfaceStates reads the state of one issue twice, as the API reports it
// and as the dashboard shows it, from the control plane at base with the
// Owner token. Both surfaces render one service, so the two must agree; a
// caller compares them.
func SurfaceStates(ctx context.Context, base, ownerToken, issueID string) (api, dashboard string, err error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	escaped := url.PathEscape(issueID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/issues/"+escaped, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	body, err := fetch(client, req, http.StatusOK)
	if err != nil {
		return "", "", fmt.Errorf("API: %w", err)
	}
	var detail struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return "", "", fmt.Errorf("API answer: %w", err)
	}

	login, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/login", strings.NewReader(url.Values{"token": {ownerToken}}.Encode()))
	if err != nil {
		return "", "", err
	}
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(login)
	if err != nil {
		return "", "", fmt.Errorf("dashboard login: %w", err)
	}
	_ = resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "sisf_session" {
			session = c
		}
	}
	if resp.StatusCode != http.StatusSeeOther || session == nil {
		return "", "", fmt.Errorf("dashboard login answered %d without a session", resp.StatusCode)
	}
	page, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/issues/"+escaped, nil)
	if err != nil {
		return "", "", err
	}
	page.AddCookie(session)
	body, err = fetch(client, page, http.StatusOK)
	if err != nil {
		return "", "", fmt.Errorf("dashboard: %w", err)
	}
	match := shownState.FindSubmatch(body)
	if match == nil {
		return "", "", fmt.Errorf("the dashboard page shows no state")
	}
	return detail.State, html.UnescapeString(string(match[1])), nil
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
	api, dashboard, err := SurfaceStates(context.Background(), f.controlSrv.URL, f.ownerToken, f.Issue(number).String())
	if err != nil {
		f.t.Fatalf("harness: %v", err)
	}
	return api, dashboard
}
