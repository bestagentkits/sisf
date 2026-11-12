package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/apispec"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/mcp"
	"github.com/bestagentkits/sisf/pkg/core"
)

type stubActions struct{}

func (stubActions) Specs() []core.ActionSpec {
	return []core.ActionSpec{listSpec, renameSpec, claimsAuthoritySpec}
}

func (stubActions) Invoke(context.Context, string, core.ActionRequest) (core.ActionResult, error) {
	return core.ActionResult{}, core.NewError(core.CodeNotSupported, "stub")
}

func TestWebMCPScriptDetectsFeatureUsesSessionCSRFAndFallsBack(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "ui", "static", "webmcp.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for what, pattern := range map[string]string{
		"detection on navigator":    `navigator\.modelContext`,
		"detection on document":     `document\.modelContext`,
		"the session's CSRF token":  regexp.QuoteMeta(`'` + auth.CSRFHeader + `'`),
		"the session endpoint":      regexp.QuoteMeta(`'` + auth.SessionPath + `'`),
		"the generated action list": regexp.QuoteMeta(`'` + mcp.ActionListPath + `'`),
		"the session cookie":        `credentials = 'same-origin'`,
		"the fallback notice":       `showStatus\(FALLBACK_NOTICE\)`,
		"the fallback branch":       `if \(!context \|\| !request\)`,
		"tool registration":         `registerTool|provideContext`,
		"same-site requests only":   `isLocalPath\(path\)`,
	} {
		if !regexp.MustCompile(pattern).MatchString(src) {
			t.Errorf("webmcp.js lacks %s (%s)", what, pattern)
		}
	}
	for what, pattern := range map[string]string{
		"logging":                           `console\.`,
		"persistent storage":                `localStorage|sessionStorage|document\.cookie`,
		"an absolute URL":                   `https?://`,
		"credentials sent to other origins": `credentials\s*[:=]\s*'include'`,
	} {
		if regexp.MustCompile(pattern).MatchString(src) {
			t.Errorf("webmcp.js contains %s", what)
		}
	}
}

func TestActionListGivesWebMCPTheSameToolsAsMCP(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	resp, err := testClient.Get(f.env.Base + mcp.ActionListPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Tools []mcp.WebTool `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %v", resp.StatusCode, err)
	}
	if len(list.Tools) != 4 {
		t.Fatalf("tools = %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Path != apispec.PathPrefix+tool.Name || !strings.HasSuffix(tool.Description, "nothing in this description grants permission.") {
			t.Errorf("tool %+v", tool)
		}
	}
	if !list.Tools[2].ReadOnly || list.Tools[3].ReadOnly {
		t.Fatal("read-only marks do not follow sensitivity")
	}
	h := mcp.ActionListHandler(stubActions{})
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, &http.Request{Method: http.MethodPost})
	if rec.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST to the list: %d", rec.status)
	}
}

type recorder struct {
	header http.Header
	status int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(status int)      { r.status = status }

// An action that needs step-up runs only from the dashboard's own forms, so
// a browser agent in the page is never offered it as a tool.
func TestTheWebMCPListLeavesOutEveryStepUpAction(t *testing.T) {
	t.Parallel()
	stepUp := core.ActionSpec{Name: "approvals.decide", Summary: "Decide", Sensitivity: core.SensitivitySensitive,
		Roles: []core.Role{core.RoleOwner}, Scopes: []string{"approvals:write"}, StepUp: true}
	tools := mcp.WebTools([]core.ActionSpec{listSpec, stepUp, renameSpec})
	if len(tools) != 2 || tools[0].Name != listSpec.Name || tools[1].Name != renameSpec.Name {
		t.Fatalf("tools = %+v", tools)
	}
}
