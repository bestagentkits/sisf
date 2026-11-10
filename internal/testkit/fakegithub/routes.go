package fakegithub

import (
	"errors"
	"net/http"
	"strings"
)

// routes registers the REST endpoints the factory uses, with GitHub's paths.
func (s *Server) routes() {
	m := s.mux
	const repoPath = "/repos/{owner}/{repo}"
	m.HandleFunc("GET "+repoPath, s.getRepo)
	m.HandleFunc("GET "+repoPath+"/issues", s.listIssues)
	m.HandleFunc("POST "+repoPath+"/issues", s.createIssue)
	m.HandleFunc("GET "+repoPath+"/issues/{number}", s.getIssue)
	m.HandleFunc("PATCH "+repoPath+"/issues/{number}", s.patchIssue)
	m.HandleFunc("POST "+repoPath+"/issues/{number}/labels", s.addLabels)
	m.HandleFunc("DELETE "+repoPath+"/issues/{number}/labels/{name}", s.removeLabel)
	m.HandleFunc("GET "+repoPath+"/issues/{number}/comments", s.listComments)
	m.HandleFunc("POST "+repoPath+"/issues/{number}/comments", s.createComment)
	m.HandleFunc("PATCH "+repoPath+"/issues/comments/{id}", s.patchComment)
	m.HandleFunc("GET "+repoPath+"/pulls/{number}", s.getPull)
	m.HandleFunc("POST "+repoPath+"/pulls", s.createPull)
	m.HandleFunc("POST "+repoPath+"/check-runs", s.createCheckRun)
	m.HandleFunc("PATCH "+repoPath+"/check-runs/{id}", s.patchCheckRun)
	m.HandleFunc("POST "+repoPath+"/releases", s.createRelease)
	m.HandleFunc("GET "+repoPath+"/contents/{path...}", s.getContents)
	m.HandleFunc("GET /app/hook/deliveries", s.listDeliveries)
	m.HandleFunc("POST /app/hook/deliveries/{id}/attempts", s.redeliver)
	m.HandleFunc("POST /_sim/repos", s.simRepo)
	m.HandleFunc("POST /_sim/webhook", s.simWebhook)
	m.HandleFunc("POST /_sim/faults", s.simFaults)
	m.HandleFunc("POST /_sim/commits", s.simCommit)
}

// actor returns the account a request acts as: the bot, or the account named
// by ActorHeader.
func (s *Server) actor(r *http.Request) User {
	spec := r.Header.Get(ActorHeader)
	if spec == "" {
		return s.bot
	}
	login, assoc, _ := strings.Cut(spec, ":")
	return s.Person(login, assoc)
}

// repoName returns "owner/name" from the request path.
func repoName(r *http.Request) string { return r.PathValue("owner") + "/" + r.PathValue("repo") }

// fail maps a store error to the status GitHub would use.
func fail(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeError(w, http.StatusUnprocessableEntity, err.Error())
}
