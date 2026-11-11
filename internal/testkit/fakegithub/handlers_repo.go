package fakegithub

import (
	"hash/fnv"
	"net/http"
)

// getRepo answers GitHub's "get a repository" for a repository the fake
// holds. The installation token of the factory may write to it, so the
// permissions say so, as GitHub reports them to an installation token.
func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repository, err := s.repoFor(repoName(r))
	var owner, name string
	var push bool
	if err == nil {
		owner, name, push = repository.owner, repository.name, !repository.readOnly
	}
	s.mu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": repoID(owner + "/" + name), "name": name, "full_name": owner + "/" + name, "private": true,
		"owner":       map[string]any{"login": owner},
		"permissions": map[string]bool{"admin": false, "maintain": false, "push": push, "triage": push, "pull": true},
	})
}

// SetWriteAccess gives the token the fake answers for write access to the
// repository, or takes it away, as an operator whose access is downgraded
// to read sees it. Only the repository's reported permissions change.
func (s *Server) SetWriteAccess(repoName string, write bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repoFor(repoName)
	if err != nil {
		return err
	}
	r.readOnly = !write
	return nil
}

// repoID is the repository's numeric ID, as GitHub gives every repository
// one: stable for a name, and never zero.
func repoID(fullName string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fullName))
	return int64(h.Sum32()) + 1
}
