package stages

import (
	"errors"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RefusalReason is why the builder refused a patch. Only a fault of the
// patch itself is a refusal: a patch that cannot be applied, or whose build
// command ran and failed. A build that could not be run at all, an outage
// of GitHub, Docker or the configuration, never is.
type RefusalReason string

const (
	// RefusalUnreadable: the patch is not a unified diff the builder reads.
	RefusalUnreadable RefusalReason = "unreadable"
	// RefusalDoesNotApply: the patch does not apply to the base commit.
	RefusalDoesNotApply RefusalReason = "does_not_apply"
	// RefusalProtectedPath: the patch touches a path ProtectedPath names.
	RefusalProtectedPath RefusalReason = "protected_path"
	// RefusalUnitSpan: the patch spans runtime units or touches no path.
	RefusalUnitSpan RefusalReason = "unit_span"
	// RefusalAttemptMismatch: the patch document names another attempt
	// than the state record of its run.
	RefusalAttemptMismatch RefusalReason = "attempt_mismatch"
	// RefusalBuildFailed: the sandbox ran the build command on the
	// candidate and its build phase exited non-zero, for example because
	// the code does not compile.
	RefusalBuildFailed RefusalReason = "build_failed"
	// RefusalBuildTimeout: the build command ran on the candidate past its
	// timeout, in any of its phases.
	RefusalBuildTimeout RefusalReason = "build_timeout"
	// RefusalNoImageReported: an image builder built the candidate without
	// naming a valid image and digest. The sandboxed build reports its image
	// from a phase that runs no code of the candidate, so a report it does
	// not make there is an outage of the build command, not this refusal.
	RefusalNoImageReported RefusalReason = "no_image_reported"
	// RefusalPathRejected: the patch names a path no commit can hold, such
	// as one inside .git, or GitHub refused the tree of its paths.
	RefusalPathRejected RefusalReason = "path_rejected"
	// RefusalNotARegularFile: the patch edits a path the base holds as a
	// directory, a submodule or a link.
	RefusalNotARegularFile RefusalReason = "not_a_regular_file"
	// RefusalBaseFileTooLarge: the patch edits a base file larger than
	// GitHub's contents API returns (1 MB).
	RefusalBaseFileTooLarge RefusalReason = "base_file_too_large"
	// RefusalCommitTooLarge: GitHub refused the patch's files as too large.
	RefusalCommitTooLarge RefusalReason = "commit_too_large"
)

// PatchRefusal is a build failure that lies in the patch. The build stage
// charges it to the run's attempt; every other build error is retried
// without spending one.
type PatchRefusal struct {
	Reason RefusalReason
	// Path is the offending path, when there is one.
	Path string
	err  *core.Error
}

// Error renders the refusal with its code.
func (r *PatchRefusal) Error() string { return r.err.Error() }

// Unwrap exposes the typed error, so core.CodeOf reads its code.
func (r *PatchRefusal) Unwrap() error { return r.err }

func refuse(code core.ErrorCode, reason RefusalReason, path, message string) *PatchRefusal {
	e := core.NewError(code, message)
	e.Details = map[string]string{"reason": string(reason)}
	if path != "" {
		e.Details["path"] = path
	}
	return &PatchRefusal{Reason: reason, Path: path, err: e}
}

// RefusalOf returns the refusal err carries, if it is one.
func RefusalOf(err error) (*PatchRefusal, bool) {
	var r *PatchRefusal
	ok := errors.As(err, &r)
	return r, ok
}
