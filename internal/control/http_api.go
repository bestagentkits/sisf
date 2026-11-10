package control

import (
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
)

// handleHealthz answers the unauthenticated liveness check. It reports each
// zone's verdict and nothing else; reasons and counts need the Owner token.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	zones := map[core.Zone]HealthStatus{}
	status, code := "ok", http.StatusOK
	for _, z := range s.svc.Health(r.Context()) {
		zones[z.Zone] = z.Status
		if z.Status != HealthOK {
			status, code = "degraded", http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, map[string]any{"status": status, "zones": zones})
}

func (s *Server) apiFactoryStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.svc.FactoryStatus(r.Context())
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) apiListIssues(w http.ResponseWriter, r *http.Request) {
	query, err := ParseIssueQuery(r.URL.Query())
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	list, err := s.svc.ListIssues(r.Context(), query)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) apiGetIssue(w http.ResponseWriter, r *http.Request) {
	ref, err := core.ParseIssueRef(r.PathValue("id"))
	if err != nil {
		s.writeAPIError(w, r, core.NewError(core.CodeInvalidArgument, "the issue id must look like owner/repo#number",
			core.NextAction{Action: "issues.list", Description: "list the tracked issues to find a valid id"}))
		return
	}
	detail, err := s.svc.GetIssue(r.Context(), ref)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeAPIError(w, r, core.NewError(core.CodeNotFound, "no such API route",
		core.NextAction{Action: "factory.status", Description: "GET /api/v1/factory/status"}))
}
