package fakegithub

import (
	"encoding/base64"
	"maps"
	"net/http"
	"path"
	"slices"

	"github.com/bestagentkits/sisf/pkg/github"
)

// EntrySymlink makes a file of a commit list in its tree as a symbolic
// link; see SetEntryKind.
const EntrySymlink = "symlink"

type jTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha,omitempty"`
	Size int    `json:"size,omitempty"`
}

// getTree answers the recursive tree of a commit made with Commit: every
// file as a blob, the directories above them, and the kinds SetEntryKind
// gave. TruncateTrees makes it answer as a tree too large to list whole.
func (s *Server) getTree(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	s.mu.Lock()
	repository, err := s.repoFor(repoName(r))
	var files map[string][]byte
	var kinds map[string]string
	truncated := s.truncateTrees
	if err == nil {
		files, kinds = repository.commits[sha], repository.entryKinds
	}
	s.mu.Unlock()
	if err != nil || files == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	dirs := map[string]bool{}
	var entries []jTreeEntry
	for _, p := range slices.Sorted(maps.Keys(files)) {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
		e := jTreeEntry{Path: p, Mode: "100644", Type: "blob", SHA: github.GitBlobSHA(files[p]), Size: len(files[p])}
		switch kinds[sha+":"+p] {
		case EntrySubmodule:
			e.Mode, e.Type, e.Size = "160000", "commit", 0
		case EntrySymlink:
			e.Mode = "120000"
		}
		entries = append(entries, e)
	}
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		entries = append(entries, jTreeEntry{Path: d, Mode: "040000", Type: "tree"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "tree": entries, "truncated": truncated})
}

// TruncateTrees makes every tree listing answer as cut short, as GitHub
// answers a tree too large to send whole.
func (s *Server) TruncateTrees() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.truncateTrees = true
}

// getBlob answers a blob of any commit made with Commit, by its git name.
func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	s.mu.Lock()
	repository, err := s.repoFor(repoName(r))
	var found []byte
	ok := false
	if err == nil {
		for _, files := range repository.commits {
			for _, content := range files {
				if github.GitBlobSHA(content) == sha {
					found, ok = content, true
				}
			}
		}
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "size": len(found), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(found)})
}
