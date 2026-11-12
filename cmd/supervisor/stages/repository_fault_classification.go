package stages

import (
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// noFault marks a GitHub refusal that is not the patch's fault: an outage
// of GitHub, the network, the credentials or the configuration, retried
// without spending an attempt.
const noFault RefusalReason = ""

// checkBase marks a refusal that is the patch's fault only when the base
// commit can itself be read: a file the base lacks, versus a repository
// the token cannot see, which GitHub answers alike.
const checkBase RefusalReason = "check_base"

// statusOutcomes decides, for each step of the builder's use of GitHub and
// each status GitHub may answer it with, whether the refusal lies in the
// patch's content. Only three do:
//
//   - a base file the patch edits that the base lacks (404 on the file,
//     with the base commit readable);
//   - a tree GitHub refuses for its paths (422 on the tree, with the base
//     commit readable: a base tree that is gone is answered alike);
//   - a tree too large for GitHub (413 on the tree).
//
// Everything else is the factory's: 401 and 403 are its credentials, 404
// on the base commit, the commit or the branch is its configuration, 409
// a concurrent or empty repository, 422 on the commit or branch a branch
// protection rule or the builder's own request, and 429 and 5xx are
// GitHub's capacity. A status not listed is an outage too: outages are
// retried a bounded number of times and then held for the Owner, so an
// unforeseen answer never spends an agent's attempt.
var statusOutcomes = map[string]map[int]RefusalReason{
	github.StepReadFile: {
		400: noFault, 401: noFault, 403: noFault, 404: checkBase, 409: noFault, 410: noFault,
		413: noFault, 422: noFault, 429: noFault, 500: noFault, 502: noFault, 503: noFault, 504: noFault,
	},
	github.StepBaseCommit: {
		400: noFault, 401: noFault, 403: noFault, 404: noFault, 409: noFault, 410: noFault,
		413: noFault, 422: noFault, 429: noFault, 500: noFault, 502: noFault, 503: noFault, 504: noFault,
	},
	github.StepTree: {
		400: noFault, 401: noFault, 403: noFault, 404: noFault, 409: noFault, 410: noFault,
		413: RefusalCommitTooLarge, 422: RefusalPathRejected, 429: noFault, 500: noFault, 502: noFault, 503: noFault, 504: noFault,
	},
	github.StepCommit: {
		400: noFault, 401: noFault, 403: noFault, 404: noFault, 409: noFault, 410: noFault,
		413: noFault, 422: noFault, 429: noFault, 500: noFault, 502: noFault, 503: noFault, 504: noFault,
	},
	github.StepBranch: {
		400: noFault, 401: noFault, 403: noFault, 404: noFault, 409: noFault, 410: noFault,
		413: noFault, 422: noFault, 429: noFault, 500: noFault, 502: noFault, 503: noFault, 504: noFault,
	},
}

// repositoryOutcome is the table's decision for one step and status.
func repositoryOutcome(step string, status int) RefusalReason {
	return statusOutcomes[step][status]
}

// contentFaults are what GitHub refuses, before or on a request, because
// of the path or file the patch names.
var contentFaults = map[string]struct {
	reason RefusalReason
	code   core.ErrorCode
}{
	github.ContentNotAFile: {RefusalNotARegularFile, core.CodeInvalidArgument},
	github.ContentTooLarge: {RefusalBaseFileTooLarge, core.CodeLimitExceeded},
	github.ContentBadPath:  {RefusalPathRejected, core.CodeInvalidArgument},
}

// classifyRepositoryError turns an error of the builder's GitHub calls on
// path into a refusal when it lies in the patch's content, and returns it
// unchanged when it is an outage. A refusal of the checkBase kind is
// returned as checkBase for the builder to decide.
func classifyRepositoryError(err error, path string) (RefusalReason, error) {
	if f, ok := contentFaults[github.ContentFault(err)]; ok {
		return f.reason, refuse(f.code, f.reason, path, "GitHub cannot take the patch's "+path+": "+string(f.reason))
	}
	reason := repositoryOutcome(github.ErrorStep(err), github.ErrorStatus(err))
	switch reason {
	case noFault, checkBase:
		return reason, err
	}
	code := core.CodeInvalidArgument
	if reason == RefusalCommitTooLarge {
		code = core.CodeLimitExceeded
	}
	return reason, refuse(code, reason, path, "GitHub refused the patch's files: "+string(reason))
}
