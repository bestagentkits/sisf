package fakegithub

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, is, err := s.find(repoName(r), n)
	if err != nil || is.pr == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, pullJSON(rp, is))
}

func (s *Server) createPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	spec := PullSpec{Title: in.Title, Body: in.Body, HeadRef: in.Head, BaseRef: in.Base, Draft: in.Draft}
	// A head of "owner:branch" names a branch in that owner's fork.
	if owner, branch, forked := strings.Cut(in.Head, ":"); forked {
		spec.HeadRef, spec.HeadRepo = branch, owner+"/"+r.PathValue("repo")
	}
	if spec.HeadRepo == "" && s.openPullOn(repoName(r), spec.HeadRef, spec.BaseRef) {
		// GitHub refuses a second open pull request from the same branch.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{
			"resource": "PullRequest", "code": "custom", "message": "A pull request already exists for " + r.PathValue("owner") + ":" + spec.HeadRef + ".",
		}}})
		return
	}
	n, err := s.OpenPullRequest(repoName(r), s.actor(r), spec)
	if err != nil {
		fail(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, is, err := s.find(repoName(r), n)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pullJSON(rp, is))
}

func (s *Server) createCheckRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url"`
		Output     struct {
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if !readJSON(r, &in) || in.Name == "" || in.HeadSHA == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: name and head_sha are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.repoFor(repoName(r))
	if err != nil {
		fail(w, err)
		return
	}
	s.nextCheckID++
	c := &checkRun{ID: s.nextCheckID, Name: in.Name, HeadSHA: in.HeadSHA, Status: in.Status, Conclusion: in.Conclusion, Summary: in.Output.Summary, DetailsURL: in.DetailsURL}
	rp.checks = append(rp.checks, c)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) patchCheckRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Output     struct {
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err != nil || !readJSON(r, &in) {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.repoFor(repoName(r))
	if err != nil {
		fail(w, err)
		return
	}
	for _, c := range rp.checks {
		if c.ID == id {
			c.Status, c.Conclusion = in.Status, in.Conclusion
			if in.Output.Summary != "" {
				c.Summary = in.Output.Summary
			}
			writeJSON(w, http.StatusOK, c)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func (s *Server) createRelease(w http.ResponseWriter, r *http.Request) {
	var in release
	if !readJSON(r, &in) || in.Tag == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: tag_name is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.repoFor(repoName(r))
	if err != nil {
		fail(w, err)
		return
	}
	for _, existing := range rp.releases {
		if existing.Tag == in.Tag {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"message": "Validation Failed",
				"errors":  []map[string]string{{"resource": "Release", "code": "already_exists", "field": "tag_name"}},
			})
			return
		}
	}
	rp.releases = append(rp.releases, &in)
	writeJSON(w, http.StatusCreated, in)
}

// openPullOn reports whether the repository has an open pull request from
// its own branch head to base.
func (s *Server) openPullOn(repoName, head, base string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.repoFor(repoName)
	if err != nil {
		return false
	}
	for _, is := range rp.issues {
		if is.pr != nil && is.state == "open" && is.pr.headOwner == rp.owner && is.pr.headName == rp.name && is.pr.headRef == head && is.pr.baseRef == base {
			return true
		}
	}
	return false
}
