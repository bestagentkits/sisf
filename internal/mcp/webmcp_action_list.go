package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/bestagentkits/sisf/internal/actions/apispec"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ActionListPath is where the browser's WebMCP script reads the tools it
// registers.
const ActionListPath = "/webmcp/actions.json"

// WebTool is one action as the WebMCP script registers it: the same name,
// description and input schema as the MCP tool, and the REST path the
// browser calls with its session and CSRF token.
type WebTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	ReadOnly    bool            `json:"readOnly"`
	Path        string          `json:"path"`
}

// WebTools lists the tools of specs, in their order, leaving out every
// action that needs step-up. Those run only from the dashboard's own forms
// after the person confirms that exact call, so a browser agent in the
// page is never offered them; the action service refuses them on the REST
// surface the WebMCP script calls as well.
func WebTools(specs []core.ActionSpec) []WebTool {
	out := make([]WebTool, 0, len(specs))
	for _, spec := range specs {
		if spec.StepUp {
			continue
		}
		out = append(out, WebTool{Name: spec.Name, Description: Describe(spec), InputSchema: InputSchema(spec),
			ReadOnly: !spec.Mutates(), Path: apispec.PathPrefix + spec.Name})
	}
	return out
}

// ActionListHandler serves the generated tool list for the WebMCP script.
// It describes actions and carries no data and no credential; it is the
// same information as the published OpenAPI document. Calling a tool still
// needs the person's session and CSRF token on the REST surface.
func ActionListHandler(a Actions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := json.Marshal(map[string]any{"tools": WebTools(a.Specs())})
		if err != nil {
			http.Error(w, "the tool list could not be encoded", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(raw)
	})
}
