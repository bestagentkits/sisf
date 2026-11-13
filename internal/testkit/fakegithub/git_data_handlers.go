package fakegithub

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

type jObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type,omitempty"`
}

type jRef struct {
	Ref    string  `json:"ref"`
	Object jObject `json:"object"`
}

// getRef answers GET /git/ref/heads/{branch}.
func (g *GitData) getRef(w http.ResponseWriter, r *http.Request) {
	branch := r.PathValue("branch")
	g.mu.Lock()
	sha, ok := g.refs[strings.ToLower(repoName(r))][branch]
	g.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, jRef{Ref: "refs/heads/" + branch, Object: jObject{SHA: sha, Type: "commit"}})
}

// getCommit answers GET /git/commits/{sha}; the tree of a commit made with
// Server.Commit is named by the commit's own SHA.
func (g *GitData) getCommit(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	if _, ok := g.files(repoName(r), sha); !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	g.mu.Lock()
	tree, ok := g.commitTree[strings.ToLower(repoName(r))+"@"+sha]
	parents := make([]map[string]string, 0, 2)
	for _, p := range g.parents[strings.ToLower(repoName(r))+"@"+sha] {
		parents = append(parents, map[string]string{"sha": p})
	}
	g.mu.Unlock()
	if !ok {
		tree = sha
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "tree": jObject{SHA: tree}, "parents": parents})
}

// createTree answers POST /git/trees with inline file contents on top of
// base_tree. An entry whose sha is explicitly null deletes the path.
func (g *GitData) createTree(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path    string  `json:"path"`
			Mode    string  `json:"mode"`
			Type    string  `json:"type"`
			Content *string `json:"content"`
		} `json:"tree"`
	}
	if !readJSON(r, &in) || len(in.Tree) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "Problems parsing JSON")
		return
	}
	files := map[string][]byte{}
	if in.BaseTree != "" {
		base, ok := g.files(repoName(r), in.BaseTree)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "base_tree is not a valid tree")
			return
		}
		files = maps.Clone(base)
	}
	for _, entry := range in.Tree {
		// GitHub refuses a tree that would hold a .git directory.
		if !validPath(entry.Path) || entry.Type != "blob" || hasGitSegment(entry.Path) {
			writeError(w, http.StatusUnprocessableEntity, "tree entries must be blobs at plain paths")
			return
		}
		if entry.Content == nil {
			delete(files, entry.Path)
			continue
		}
		files[entry.Path] = []byte(*entry.Content)
	}
	sha := treeSHA(files)
	g.mu.Lock()
	g.trees[sha] = files
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, jObject{SHA: sha})
}

// createCommit answers POST /git/commits. The commit is recorded in the
// Server too, so the contents API serves its files.
func (g *GitData) createCommit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if !readJSON(r, &in) || in.Message == "" || in.Tree == "" {
		writeError(w, http.StatusUnprocessableEntity, "a commit needs a message and a tree")
		return
	}
	files, ok := g.files(repoName(r), in.Tree)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "tree is not a valid tree")
		return
	}
	for _, parent := range in.Parents {
		if _, ok := g.files(repoName(r), parent); !ok {
			writeError(w, http.StatusUnprocessableEntity, "parent is not a valid commit")
			return
		}
	}
	text := make(map[string]string, len(files))
	for path, content := range files {
		text[path] = string(content)
	}
	sha, err := g.s.commitOn(repoName(r), text, in.Parents)
	if err != nil {
		fail(w, err)
		return
	}
	g.mu.Lock()
	g.commitTree[strings.ToLower(repoName(r))+"@"+sha] = in.Tree
	if key := strings.ToLower(repoName(r)) + "@" + sha; g.parents[key] == nil {
		g.parents[key] = in.Parents
	}
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"sha": sha, "tree": jObject{SHA: in.Tree}})
}

// createRef answers POST /git/refs; an existing branch is a 422.
func (g *GitData) createRef(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	branch, isBranch := "", false
	if readJSON(r, &in) {
		branch, isBranch = strings.CutPrefix(in.Ref, "refs/heads/")
	}
	if !isBranch || branch == "" {
		writeError(w, http.StatusUnprocessableEntity, "ref must be refs/heads/<branch>")
		return
	}
	g.mu.Lock()
	_, exists := g.refs[strings.ToLower(repoName(r))][branch]
	g.mu.Unlock()
	if exists {
		writeError(w, http.StatusUnprocessableEntity, "Reference already exists")
		return
	}
	if err := g.SetBranch(repoName(r), branch, in.SHA); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Object does not exist")
		return
	}
	writeJSON(w, http.StatusCreated, jRef{Ref: in.Ref, Object: jObject{SHA: in.SHA, Type: "commit"}})
}

// updateRef answers PATCH /git/refs/heads/{branch}.
func (g *GitData) updateRef(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SHA string `json:"sha"`
	}
	branch := r.PathValue("branch")
	if !readJSON(r, &in) || g.SetBranch(repoName(r), branch, in.SHA) != nil {
		writeError(w, http.StatusUnprocessableEntity, "Object does not exist")
		return
	}
	writeJSON(w, http.StatusOK, jRef{Ref: "refs/heads/" + branch, Object: jObject{SHA: in.SHA, Type: "commit"}})
}

// compare answers GET /compare/{base}...{head} with the paths head
// changed since it parted from base. Like GitHub, base and head are each a
// commit or a branch name, and the paths are those that differ between
// head and the merge base of the two: none when base contains head. For
// commits whose history this fake does not know, made with Server.Commit,
// base itself stands for the merge base. As GitHub does for an exact
// rename, a removed file whose content an added file has is one entry,
// "renamed", naming the added path and, as previous_filename, the removed
// one.
func (g *GitData) compare(w http.ResponseWriter, r *http.Request) {
	base, head, ok := strings.Cut(r.PathValue("basehead"), "...")
	baseSHA, headSHA := g.revision(repoName(r), base), g.revision(repoName(r), head)
	headFiles, okHead := g.files(repoName(r), headSHA)
	baseFiles, okBase := g.files(repoName(r), g.mergeBase(repoName(r), baseSHA, headSHA))
	if !ok || !okBase || !okHead {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	type jFile struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename,omitempty"`
		Status           string `json:"status"`
	}
	files := []jFile{}
	var added []string
	for _, path := range slices.Sorted(maps.Keys(headFiles)) {
		before, existed := baseFiles[path]
		switch {
		case !existed:
			added = append(added, path)
		case string(before) != string(headFiles[path]):
			files = append(files, jFile{Filename: path, Status: "modified"})
		}
	}
	for _, path := range slices.Sorted(maps.Keys(baseFiles)) {
		if _, kept := headFiles[path]; kept {
			continue
		}
		if i := slices.IndexFunc(added, func(to string) bool { return string(headFiles[to]) == string(baseFiles[path]) }); i >= 0 {
			files = append(files, jFile{Filename: added[i], PreviousFilename: path, Status: "renamed"})
			added = slices.Delete(added, i, i+1)
			continue
		}
		files = append(files, jFile{Filename: path, Status: "removed"})
	}
	for _, path := range added {
		files = append(files, jFile{Filename: path, Status: "added"})
	}
	g.mu.Lock()
	beyond := g.beyondLimit
	g.mu.Unlock()
	for n := len(files); beyond && n < compareFileLimit; n++ {
		files = append(files, jFile{Filename: fmt.Sprintf("listed-beyond/%03d.txt", n), Status: "added"})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_commits": g.commitsAhead(repoName(r), baseSHA, headSHA),
		"files":         files[:min(len(files), compareFileLimit)],
	})
}

// commitsAhead counts the commits head has that base does not, GitHub's
// total_commits of a comparison.
func (g *GitData) commitsAhead(repoName, base, head string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.ToLower(repoName) + "@"
	reach := func(from string) map[string]bool {
		seen := map[string]bool{from: true}
		for queue := []string{from}; len(queue) > 0; queue = queue[1:] {
			for _, p := range g.parents[key+queue[0]] {
				if !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
		return seen
	}
	ofBase, ahead := reach(base), 0
	for c := range reach(head) {
		if !ofBase[c] {
			ahead++
		}
	}
	return ahead
}

// compareFileLimit is how many files GitHub lists in a comparison at most.
const compareFileLimit = 300

// CompareBeyondTheLimit makes every later comparison answer as GitHub does
// for one of more files than it lists: exactly compareFileLimit files, the
// paths that really differ first and made-up ones after them. A candidate
// patch is too small to reach the limit, so this is how a test sees it.
func (g *GitData) CompareBeyondTheLimit() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.beyondLimit = true
}

// revision is the commit a branch name points at, or rev itself when no
// branch has that name.
func (g *GitData) revision(repoName, rev string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sha, ok := g.refs[strings.ToLower(repoName)][rev]; ok {
		return sha
	}
	return rev
}

// mergeBase is a best common ancestor of base and head, as git's merge
// base: a commit both descend from, each counted as descending from itself,
// that no other such commit descends from. A search's first common commit
// is not enough: after a merge whose first parent is the commit a branch
// was made on, that commit is reached before the branch. It is base when
// the history this fake knows has none.
func (g *GitData) mergeBase(repoName, base, head string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.ToLower(repoName) + "@"
	ancestors := func(from string) map[string]bool {
		seen := map[string]bool{from: true}
		for queue := []string{from}; len(queue) > 0; queue = queue[1:] {
			for _, p := range g.parents[key+queue[0]] {
				if !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
		return seen
	}
	ofBase, ofHead := ancestors(base), ancestors(head)
	var common []string
	for c := range ofHead {
		if ofBase[c] {
			common = append(common, c)
		}
	}
	slices.Sort(common) // a stable answer when several are best
	for _, c := range common {
		best := true
		for _, other := range common {
			if other != c && ancestors(other)[c] {
				best = false
				break
			}
		}
		if best {
			return c
		}
	}
	return base
}
