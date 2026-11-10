package fakegithub

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// paginate cuts items to the requested page and sets the Link header.
func paginate[T any](w http.ResponseWriter, r *http.Request, items []T) []T {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 {
		perPage = 30
	}
	perPage = min(perPage, 100)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	start := min((page-1)*perPage, len(items))
	end := min(start+perPage, len(items))
	if end < len(items) {
		next := *r.URL
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s>; rel="next"`, r.Host, next.RequestURI()))
	}
	return items[start:end]
}

func parseSince(r *http.Request) time.Time {
	since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	return since
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	var want []string
	if l := q.Get("labels"); l != "" {
		want = strings.Split(l, ",")
	}
	since := parseSince(r)
	s.mu.Lock()
	rp, err := s.repoFor(repoName(r))
	if err != nil {
		s.mu.Unlock()
		fail(w, err)
		return
	}
	var matched []*issue
	for _, is := range rp.issues {
		ok := (state == "all" || is.state == state) && !is.updated.Before(since)
		for _, l := range want {
			ok = ok && is.hasLabel(l)
		}
		if ok {
			matched = append(matched, is)
		}
	}
	sortKey := func(is *issue) time.Time { return is.created }
	if q.Get("sort") == "updated" {
		sortKey = func(is *issue) time.Time { return is.updated }
	}
	sort.Slice(matched, func(i, j int) bool {
		a, b := sortKey(matched[i]), sortKey(matched[j])
		if a.Equal(b) {
			return matched[i].number < matched[j].number
		}
		return a.Before(b)
	})
	if q.Get("direction") != "asc" {
		slices.Reverse(matched)
	}
	page := []jIssue{}
	for _, is := range paginate(w, r, matched) {
		page = append(page, issueJSON(is))
	}
	s.mu.Unlock()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(page)
	sum := sha1.Sum(buf.Bytes())
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if !readJSON(r, &in) || strings.TrimSpace(in.Title) == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: title is required")
		return
	}
	n, err := s.OpenIssue(repoName(r), s.actor(r), in.Title, in.Body, in.Labels...)
	if err != nil {
		fail(w, err)
		return
	}
	s.respondIssue(w, r, n, http.StatusCreated)
}

// respondIssue writes the current state of an issue.
func (s *Server) respondIssue(w http.ResponseWriter, r *http.Request, number, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, is, err := s.find(repoName(r), number)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, status, issueJSON(is))
}

func issueNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n < 1 {
		writeError(w, http.StatusNotFound, "Not Found")
		return 0, false
	}
	return n, true
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	if n, ok := issueNumber(w, r); ok {
		s.respondIssue(w, r, n, http.StatusOK)
	}
}

func (s *Server) patchIssue(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	var in struct {
		State string `json:"state"`
	}
	if !ok {
		return
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	if err := s.SetIssueState(repoName(r), n, s.actor(r), in.State); err != nil {
		fail(w, err)
		return
	}
	s.respondIssue(w, r, n, http.StatusOK)
}

func (s *Server) addLabels(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	var in struct {
		Labels []string `json:"labels"`
	}
	if !ok {
		return
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	if err := s.AddLabels(repoName(r), n, s.actor(r), in.Labels...); err != nil {
		fail(w, err)
		return
	}
	s.respondLabels(w, r, n)
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	if !ok {
		return
	}
	if err := s.RemoveLabel(repoName(r), n, s.actor(r), r.PathValue("name")); err != nil {
		fail(w, err)
		return
	}
	s.respondLabels(w, r, n)
}

func (s *Server) respondLabels(w http.ResponseWriter, r *http.Request, number int) {
	view, err := s.Issue(repoName(r), number)
	if err != nil {
		fail(w, err)
		return
	}
	out := []jLabel{}
	for _, l := range view.Labels {
		out = append(out, jLabel{Name: l})
	}
	writeJSON(w, http.StatusOK, out)
}
