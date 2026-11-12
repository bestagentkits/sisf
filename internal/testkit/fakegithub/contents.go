package fakegithub

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// maxCommitFiles and maxFileBytes bound one commit made through the
// simulation API, so a test cannot make the fake hold unbounded data.
const (
	maxCommitFiles = 64
	maxFileBytes   = 1 << 20
	maxCommitBody  = 8 << 20
)

// Commit records files as a new commit of a repository and returns its SHA.
// The SHA is derived from the paths and contents, so the same files always
// make the same commit. Files of earlier commits are not carried over: a
// commit holds exactly the files given.
func (s *Server) Commit(repoName string, files map[string]string) (string, error) {
	if len(files) == 0 || len(files) > maxCommitFiles {
		return "", fmt.Errorf("a commit holds between 1 and %d files", maxCommitFiles)
	}
	h := sha1.New()
	stored := make(map[string][]byte, len(files))
	for _, path := range slices.Sorted(maps.Keys(files)) {
		content := files[path]
		if !validPath(path) {
			return "", fmt.Errorf("path %q is not a plain relative path", path)
		}
		if len(content) > maxFileBytes {
			return "", fmt.Errorf("file %q is larger than %d bytes", path, maxFileBytes)
		}
		_, _ = fmt.Fprintf(h, "%q %d\n%s\n", path, len(content), content) // a hash never fails to write
		stored[path] = []byte(content)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repoFor(repoName)
	if err != nil {
		return "", err
	}
	if r.commits == nil {
		r.commits = map[string]map[string][]byte{}
	}
	r.commits[sha] = stored
	return sha, nil
}

// validPath accepts a relative path without empty, "." or ".." segments.
func validPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// jContent is the answer of the contents API for one file.
type jContent struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Content  string `json:"content"`
}

// getContents serves one file at the commit named by ref, as GitHub's
// contents API does. Only full commit SHAs made with Commit are known; a
// branch name or any other ref is not found.
func (s *Server) getContents(w http.ResponseWriter, r *http.Request) {
	path, ref := r.PathValue("path"), r.URL.Query().Get("ref")
	s.mu.Lock()
	repository, err := s.repoFor(repoName(r))
	var content []byte
	found := false
	if err == nil {
		content, found = repository.commits[ref][path]
	}
	s.mu.Unlock()
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	// GitHub wraps the base64 text at 60 characters; clients must cope.
	encoded := base64.StdEncoding.EncodeToString(content)
	var wrapped strings.Builder
	for len(encoded) > 60 {
		wrapped.WriteString(encoded[:60] + "\n")
		encoded = encoded[60:]
	}
	wrapped.WriteString(encoded)
	writeJSON(w, http.StatusOK, jContent{Type: "file", Encoding: "base64", Path: path, Size: len(content), Content: wrapped.String()})
}

// simCommit is POST /_sim/commits {"repo":"owner/name","files":{path: content}}.
func (s *Server) simCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo  string            `json:"repo"`
		Files map[string]string `json:"files"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxCommitBody)).Decode(&body) != nil {
		writeError(w, http.StatusBadRequest, "body must be {\"repo\":\"owner/name\",\"files\":{...}}")
		return
	}
	sha, err := s.Commit(body.Repo, body.Files)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"sha": sha})
}
