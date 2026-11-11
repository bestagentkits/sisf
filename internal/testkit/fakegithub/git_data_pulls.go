package fakegithub

import (
	"maps"
	"net/http"
	"strings"
)

// The pull request routes that need the branches: a pull request's head is
// where its branch points now, found again by its branch, and merged with
// GitHub's guard on the head commit.

// livePull is a pull request as GitHub shows it, its head read from the
// branch. Callers hold no lock.
func (g *GitData) livePull(repoName string, is *issue, r *repo) jPull {
	p := pullJSON(r, is)
	g.mu.Lock()
	if sha, ok := g.refs[strings.ToLower(repoName)][is.pr.headRef]; ok && !is.pr.merged {
		p.Head.SHA = sha
	}
	if is.pr.merged {
		p.Head.SHA = is.pr.headSHA
	}
	g.mu.Unlock()
	return p
}

// getPull answers GET /pulls/{number} with the head the branch has now.
func (g *GitData) getPull(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	if !ok {
		return
	}
	g.s.mu.Lock()
	rp, is, err := g.s.find(repoName(r), n)
	g.s.mu.Unlock()
	if err != nil || is.pr == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, g.livePull(repoName(r), is, rp))
}

// listPulls answers GET /pulls, filtered by state and by a head of
// "owner:branch".
func (g *GitData) listPulls(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	_, headBranch, _ := strings.Cut(r.URL.Query().Get("head"), ":")
	g.s.mu.Lock()
	rp, err := g.s.repoFor(repoName(r))
	var found []*issue
	if err == nil {
		for _, is := range rp.issues {
			if is.pr == nil || (state != "all" && is.state != state) || (headBranch != "" && is.pr.headRef != headBranch) {
				continue
			}
			found = append(found, is)
		}
	}
	g.s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	out := []jPull{}
	for _, is := range found {
		out = append(out, g.livePull(repoName(r), is, rp))
	}
	writeJSON(w, http.StatusOK, out)
}

// mergePull answers PUT /pulls/{number}/merge with a merge commit on the
// base branch. Like GitHub it refuses with 409 when sha is not the head the
// branch has now, and with 405 when the pull request is closed or the
// change conflicts with what the base branch gained since the head's
// parent.
func (g *GitData) mergePull(w http.ResponseWriter, r *http.Request) {
	n, ok := issueNumber(w, r)
	if !ok {
		return
	}
	var in struct {
		SHA string `json:"sha"`
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	name := repoName(r)
	g.s.mu.Lock()
	_, is, err := g.s.find(name, n)
	var headRef, baseRef, state string
	if err == nil && is.pr != nil {
		headRef, baseRef, state = is.pr.headRef, is.pr.baseRef, is.state
	}
	g.s.mu.Unlock()
	if err != nil || is.pr == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	g.mu.Lock()
	head, base := g.refs[strings.ToLower(name)][headRef], g.refs[strings.ToLower(name)][baseRef]
	parents := g.parents[strings.ToLower(name)+"@"+head]
	g.mu.Unlock()
	switch {
	case state != "open":
		writeError(w, http.StatusMethodNotAllowed, "Pull Request is not mergeable")
		return
	case in.SHA != "" && in.SHA != head:
		writeError(w, http.StatusConflict, "Head branch was modified. Review and try the merge again.")
		return
	}
	files, ok := g.mergeTrees(name, base, head, parents)
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "Pull Request is not mergeable")
		return
	}
	text := make(map[string]string, len(files))
	for path, content := range files {
		text[path] = string(content)
	}
	sha, err := g.s.Commit(name, text)
	if err != nil {
		fail(w, err)
		return
	}
	tree := treeSHA(files)
	g.mu.Lock()
	g.trees[tree] = files
	g.commitTree[strings.ToLower(name)+"@"+sha] = tree
	g.merges++
	g.mu.Unlock()
	if err := g.SetBranch(name, baseRef, sha); err != nil {
		fail(w, err)
		return
	}
	if err := g.s.markMerged(name, n, head, g.s.actor(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "merged": true, "message": "Pull Request successfully merged"})
}

// mergeTrees applies what head changed since its parent to base. A path
// head changed that base changed differently since that parent is a
// conflict; so is a head whose parent is not known.
func (g *GitData) mergeTrees(repoName, base, head string, parents []string) (map[string][]byte, bool) {
	baseFiles, okBase := g.files(repoName, base)
	headFiles, okHead := g.files(repoName, head)
	if !okBase || !okHead || len(parents) == 0 {
		return nil, false
	}
	parentFiles, ok := g.files(repoName, parents[0])
	if !ok {
		return nil, false
	}
	merged := maps.Clone(baseFiles)
	paths := maps.Clone(headFiles)
	maps.Copy(paths, parentFiles)
	for path := range paths {
		was, had := parentFiles[path]
		now, has := headFiles[path]
		if had == has && string(was) == string(now) {
			continue
		}
		onBase, onBaseHas := baseFiles[path]
		if (onBaseHas != had || string(onBase) != string(was)) && (onBaseHas != has || string(onBase) != string(now)) {
			return nil, false
		}
		if has {
			merged[path] = now
		} else {
			delete(merged, path)
		}
	}
	return merged, true
}

// Merges counts the pull requests merged through the API.
func (g *GitData) Merges() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.merges
}

// markMerged closes a pull request as merged at head.
func (s *Server) markMerged(repoName string, number int, head string, by User) error {
	return s.change(func() ([]pending, error) {
		r, is, err := s.find(repoName, number)
		if err != nil {
			return nil, err
		}
		is.pr.merged, is.pr.headSHA, is.state, is.updated = true, head, "closed", s.now()
		return []pending{s.event(r, "pull_request", "closed", by, map[string]any{"pull_request": pullJSON(r, is)})}, nil
	})
}

// viewer answers GET /user with the account the token acts as.
func (s *Server) viewer(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	writeJSON(w, http.StatusOK, s.actor(r).json())
}
