package fakegithub

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// GitData serves the git data API (refs, trees, commits and the compare
// endpoint) in front of a Server and hands every other request to it. A
// tree is the full set of files of a commit, so a commit made through it
// carries the files of its base forward, unlike Server.Commit.
//
// Create it with NewGitData and serve it in place of the Server.
type GitData struct {
	s   *Server
	mux *http.ServeMux

	mu    sync.Mutex
	trees map[string]map[string][]byte // tree SHA -> files
	refs  map[string]map[string]string // repo key -> branch -> commit SHA
	// commitTree maps "repo key@commit" to the tree of a commit made here.
	commitTree map[string]string
	// parents maps "repo key@commit" to the parents a commit was made with.
	parents map[string][]string
	// writes counts the requests that ask to write git data; merges the
	// pull requests merged.
	writes, merges int
	// routeFaults are canned refusals of chosen routes; see FailOn.
	routeFaults []routeFault
}

// NewGitData wraps s.
func NewGitData(s *Server) *GitData {
	g := &GitData{s: s, mux: http.NewServeMux(), trees: map[string]map[string][]byte{},
		refs: map[string]map[string]string{}, commitTree: map[string]string{}, parents: map[string][]string{}}
	const repoPath = "/repos/{owner}/{repo}"
	g.mux.HandleFunc("GET "+repoPath+"/git/ref/heads/{branch...}", g.getRef)
	g.mux.HandleFunc("GET "+repoPath+"/git/commits/{sha}", g.getCommit)
	g.mux.HandleFunc("POST "+repoPath+"/git/trees", g.createTree)
	g.mux.HandleFunc("POST "+repoPath+"/git/commits", g.createCommit)
	g.mux.HandleFunc("POST "+repoPath+"/git/refs", g.createRef)
	g.mux.HandleFunc("PATCH "+repoPath+"/git/refs/heads/{branch...}", g.updateRef)
	g.mux.HandleFunc("GET "+repoPath+"/compare/{basehead}", g.compare)
	g.mux.HandleFunc("GET "+repoPath+"/pulls", g.listPulls)
	g.mux.HandleFunc("GET "+repoPath+"/pulls/{number}", g.getPull)
	g.mux.HandleFunc("PUT "+repoPath+"/pulls/{number}/merge", g.mergePull)
	g.mux.Handle("/", s)
	return g
}

// ServeHTTP serves the git data routes and passes everything else on.
func (g *GitData) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/git/") && r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.mu.Lock()
		g.writes++
		g.mu.Unlock()
	}
	if strings.Contains(r.URL.Path, "/git/") || strings.Contains(r.URL.Path, "/compare/") || strings.Contains(r.URL.Path, "/pulls") {
		if !g.s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
			return
		}
	}
	if f, ok := g.takeRouteFault(r); ok {
		writeError(w, f.status, f.message)
		return
	}
	g.mux.ServeHTTP(w, r)
}

// Writes returns how many requests asked to write git data (trees,
// commits or refs), whether or not they were authorized.
func (g *GitData) Writes() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writes
}

// SetBranch points branch of repoName at commit, a SHA made with Commit or
// through the API.
func (g *GitData) SetBranch(repoName, branch, commit string) error {
	if _, ok := g.files(repoName, commit); !ok {
		return fmt.Errorf("commit %s is not known in %s", commit, repoName)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.ToLower(repoName)
	if g.refs[key] == nil {
		g.refs[key] = map[string]string{}
	}
	g.refs[key][branch] = commit
	return nil
}

// Branch returns the commit a branch points at and the files of that commit.
func (g *GitData) Branch(repoName, branch string) (string, map[string]string, bool) {
	g.mu.Lock()
	sha, ok := g.refs[strings.ToLower(repoName)][branch]
	g.mu.Unlock()
	if !ok {
		return "", nil, false
	}
	raw, _ := g.files(repoName, sha)
	files := make(map[string]string, len(raw))
	for path, content := range raw {
		files[path] = string(content)
	}
	return sha, files, true
}

// Branches lists the branches of repoName, sorted.
func (g *GitData) Branches(repoName string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Sorted(maps.Keys(g.refs[strings.ToLower(repoName)]))
}

// files returns the files of a commit or a tree.
func (g *GitData) files(repoName, sha string) (map[string][]byte, bool) {
	g.mu.Lock()
	if tree, ok := g.commitTree[strings.ToLower(repoName)+"@"+sha]; ok {
		sha = tree
	}
	files, ok := g.trees[sha]
	g.mu.Unlock()
	if ok {
		return files, true
	}
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	r, err := g.s.repoFor(repoName)
	if err != nil {
		return nil, false
	}
	files, ok = r.commits[sha]
	return files, ok
}

// treeSHA names a set of files by its content.
func treeSHA(files map[string][]byte) string {
	h := sha1.New()
	for _, path := range slices.Sorted(maps.Keys(files)) {
		_, _ = fmt.Fprintf(h, "%q %d\n%s\n", path, len(files[path]), files[path]) // a hash never fails to write
	}
	return hex.EncodeToString(h.Sum(nil))
}
