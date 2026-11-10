package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxFileContent is the largest file ReadFile returns, in bytes: the most the
// contents API embeds in its answer.
const maxFileContent = 1 << 20

// ReadFile returns the content of one file of repo at an exact commit. A
// branch, a tag or any other name that can move is refused before a request
// is made, because what a commit holds never changes and what a branch holds
// does. A path that leaves the repository root is refused too. A missing
// commit or file is NOT_FOUND; anything other than a plain file is
// INVALID_ARGUMENT. Every error names the step StepReadFile, and a refusal
// because of the path or the file names its ContentFault.
func (c *RESTClient) ReadFile(ctx context.Context, repo RepoRef, revision core.CommitSHA, path string) ([]byte, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if err := revision.Validate(); err != nil {
		return nil, err
	}
	if !plainPath(path) {
		return nil, contentFault(core.CodeInvalidArgument, StepReadFile, ContentBadPath, "file path "+path+" is not a plain path inside the repository")
	}
	segments := []string{"repos", repo.Owner, repo.Name, "contents"}
	segments = append(segments, strings.Split(path, "/")...)
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	resp, err := c.call(ctx, http.MethodGet, "/"+strings.Join(segments, "/"), url.Values{"ref": {string(revision)}}, nil, nil)
	if err != nil {
		return nil, atStep(err, StepReadFile)
	}
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
		Content  string `json:"content"`
	}
	// A directory is answered with the list of its entries.
	if body := bytes.TrimSpace(resp.body); len(body) > 0 && body[0] == '[' {
		return nil, contentFault(core.CodeInvalidArgument, StepReadFile, ContentNotAFile, path+" is a directory, not a file")
	}
	if err := decode(resp, &file); err != nil {
		return nil, atStep(err, StepReadFile)
	}
	switch {
	case file.Type != "file":
		return nil, contentFault(core.CodeInvalidArgument, StepReadFile, ContentNotAFile, path+" is not a file")
	case file.Encoding != "base64" || file.Size > maxFileContent:
		return nil, contentFault(core.CodeInvalidArgument, StepReadFile, ContentTooLarge, path+" is too large to read through the contents API")
	}
	content, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(file.Content))
	if err != nil || len(content) != file.Size {
		return nil, core.NewError(core.CodeUnavailable, "GitHub returned the content of "+path+" in a form that could not be read")
	}
	return content, nil
}

// plainPath accepts a relative path with no empty, "." or ".." segment.
func plainPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
