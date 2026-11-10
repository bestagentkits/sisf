package github

import (
	"errors"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Steps of ReadFile and CommitToBranch an error is tagged with, so a caller
// can tell what GitHub refused: the file asked for, the base commit, the
// tree of the new files, the commit, or the branch update.
const (
	StepReadFile   = "read_file"
	StepBaseCommit = "base_commit"
	StepTree       = "tree"
	StepCommit     = "commit"
	StepBranch     = "branch"
)

// Content faults: a request was refused, before or by GitHub, because of
// the content asked for rather than the connection or the credentials.
const (
	// ContentNotAFile: the path names a directory, a submodule or a link.
	ContentNotAFile = "not_a_file"
	// ContentTooLarge: the file is larger than the contents API returns.
	ContentTooLarge = "too_large"
	// ContentBadPath: the path is not one a Git tree can hold.
	ContentBadPath = "bad_path"
)

// Detail keys of the tags.
const (
	detailStep         = "github_step"
	detailContentFault = "content_fault"
	detailStatus       = "status"
)

// maxTreePath is the longest path the factory writes into a tree.
const maxTreePath = 4096

// ValidTreePath reports whether a commit may hold path: a plain relative
// path with no empty, ".", ".." or ".git" segment (in any letter case, as
// Git refuses it on case-insensitive file systems), no NUL or backslash,
// and at most 4096 bytes. GitHub refuses a tree with any other path.
func ValidTreePath(path string) bool {
	if !plainPath(path) || len(path) > maxTreePath || strings.ContainsRune(path, 0) {
		return false
	}
	for segment := range strings.SplitSeq(path, "/") {
		if strings.EqualFold(segment, ".git") {
			return false
		}
	}
	return true
}

// ErrorStep returns the step of ReadFile or CommitToBranch err came from,
// "" when it carries none.
func ErrorStep(err error) string { return detail(err, detailStep) }

// ErrorStatus returns the HTTP status GitHub answered with, 0 when err has
// none: a network failure, a rate limit or a refusal before any request.
func ErrorStatus(err error) int {
	n, _ := strconv.Atoi(detail(err, detailStatus))
	return n
}

// ContentFault returns the content fault err is, "" when it is none.
func ContentFault(err error) string { return detail(err, detailContentFault) }

func detail(err error, key string) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Details[key]
	}
	return ""
}

// tagged sets details on the typed error err carries and returns err.
func tagged(err error, kv ...string) error {
	var e *core.Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		e.Details[kv[i]] = kv[i+1]
	}
	return err
}

// atStep tags err with the step it came from; nil stays nil.
func atStep(err error, step string) error {
	if err == nil {
		return nil
	}
	return tagged(err, detailStep, step)
}

// contentFault is a typed refusal because of the content asked for.
func contentFault(code core.ErrorCode, step, fault, message string) error {
	return tagged(core.NewError(code, message), detailStep, step, detailContentFault, fault)
}
