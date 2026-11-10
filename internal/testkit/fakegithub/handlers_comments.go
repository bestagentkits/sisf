package fakegithub

import (
	"net/http"
	"strconv"
)

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	if !ok {
		return
	}
	since := parseSince(r)
	s.mu.Lock()
	rp, _, err := s.find(repoName(r), n)
	if err != nil {
		s.mu.Unlock()
		fail(w, err)
		return
	}
	var all []jComment
	for _, c := range rp.comments {
		if c.issue == n && !c.updated.Before(since) {
			all = append(all, commentJSON(c))
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, append([]jComment{}, paginate(w, r, all)...))
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	var in struct {
		Body string `json:"body"`
	}
	if !ok {
		return
	}
	if !readJSON(r, &in) || in.Body == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: body is required")
		return
	}
	id, err := s.AddComment(repoName(r), n, s.actor(r), in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	s.respondComment(w, r, id, http.StatusCreated)
}

func (s *Server) patchComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Body string `json:"body"`
	}
	if err != nil || !readJSON(r, &in) || in.Body == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	if err := s.EditComment(repoName(r), id, s.actor(r), in.Body); err != nil {
		fail(w, err)
		return
	}
	s.respondComment(w, r, id, http.StatusOK)
}

func (s *Server) respondComment(w http.ResponseWriter, r *http.Request, id int64, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.repoFor(repoName(r))
	if err != nil {
		fail(w, err)
		return
	}
	for _, c := range rp.comments {
		if c.id == id {
			writeJSON(w, status, commentJSON(c))
			return
		}
	}
	fail(w, errNotFound)
}
