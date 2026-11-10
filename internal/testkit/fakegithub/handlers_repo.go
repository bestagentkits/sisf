package fakegithub

import "net/http"

// getRepo answers GitHub's "get a repository" for a repository the fake
// holds. The installation token of the factory may write to it, so the
// permissions say so, as GitHub reports them to an installation token.
func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repository, err := s.repoFor(repoName(r))
	var owner, name string
	if err == nil {
		owner, name = repository.owner, repository.name
	}
	s.mu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "full_name": owner + "/" + name, "private": true,
		"owner":       map[string]any{"login": owner},
		"permissions": map[string]bool{"admin": false, "maintain": false, "push": true, "triage": true, "pull": true},
	})
}
