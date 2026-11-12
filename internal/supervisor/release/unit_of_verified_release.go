package release

import (
	"context"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// unitOfRelease reads the runtime unit the release of record is a build of
// from the build record of its candidate commit: the candidate of the
// record's run and attempt names the commit, and the build of that commit
// must be the digest the record names as both its candidate and its
// release. found is false when the candidate or the build cannot be read,
// or they name another build than the record: such a release is known to
// be a build of no unit and supersedes nothing. An error is returned only
// when the store could not be read.
func unitOfRelease(ctx context.Context, kv state.KV, record core.StateRecord) (unit string, found bool, err error) {
	doc, err := handoff.ReadCandidate(ctx, kv, record.RunID, record.Attempt)
	if skipped(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if doc.Candidate.Digest != record.CandidateDigest {
		return "", false, nil
	}
	build, err := handoff.ReadBuild(ctx, kv, doc.Candidate.CommitSHA)
	if skipped(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if build.Digest != record.CandidateDigest || core.ReleaseDigest(build.Digest) != record.ReleaseDigest || build.Unit == "" {
		return "", false, nil
	}
	return build.Unit, true, nil
}

// skipped reports whether a document that could not be read is missing or
// does not decode as what it claims to be, rather than unreadable now.
func skipped(err error) bool {
	return core.HasCode(err, core.CodeNotFound) || core.HasCode(err, core.CodeTampered)
}
