package github

import (
	"context"
	"crypto/sha1" //nolint:gosec // git names a blob by its SHA-1; it is checked, not trusted for security
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// StepBlob tags the errors of ReadBlob.
const StepBlob = "blob"

// MaxBlobBytes bounds a blob ReadBlob reads: its base64 form must fit in
// one GitHub response.
const MaxBlobBytes = maxResponseBytes / 4 * 3 / 2

var blobSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ReadBlob reads the blob named sha, as a tree listing names it. The
// content is checked against the name, so what is returned is exactly
// the blob the tree holds. A blob past MaxBlobBytes is LIMIT_EXCEEDED.
func (c *RESTClient) ReadBlob(ctx context.Context, repo RepoRef, sha string) ([]byte, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if !blobSHA.MatchString(sha) {
		return nil, core.NewError(core.CodeInvalidArgument, "blob name "+sha+" is not a SHA-1")
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(repo, "git", "blobs", sha), nil, nil, nil)
	if err != nil {
		return nil, atStep(err, StepBlob)
	}
	var blob struct {
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	if err := decode(resp, &blob); err != nil {
		return nil, atStep(err, StepBlob)
	}
	if blob.Size > MaxBlobBytes {
		return nil, core.NewError(core.CodeLimitExceeded, "blob "+sha+" is too large to read")
	}
	content, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(blob.Content))
	if err != nil || blob.Encoding != "base64" || int64(len(content)) != blob.Size || GitBlobSHA(content) != sha {
		return nil, core.NewError(core.CodeUnavailable, "GitHub returned blob "+sha+" in a form that does not match its name")
	}
	return content, nil
}

// GitBlobSHA is the name git gives a blob of content.
func GitBlobSHA(content []byte) string {
	h := sha1.New() //nolint:gosec // see the import
	h.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
