package stages

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// GitHubRepository is what the supervisor reads of the factory's repository.
type GitHubRepository interface {
	ReadFile(ctx context.Context, repo github.RepoRef, revision core.CommitSHA, path string) ([]byte, error)
	ChangedPaths(ctx context.Context, repo github.RepoRef, base, head core.CommitSHA) ([]string, error)
}

// Tree reads one repository at exact commits; it satisfies FileReader and
// supplychain.Tree. Changed paths are always read from the repository,
// never taken from what a candidate says about itself.
type Tree struct {
	GitHub GitHubRepository
	Repo   github.RepoRef
}

// ReadFile returns path at commit, NOT_FOUND when it has none.
func (t Tree) ReadFile(ctx context.Context, commit core.CommitSHA, path string) ([]byte, error) {
	return t.GitHub.ReadFile(ctx, t.Repo, commit, path)
}

// ChangedPaths lists the paths that differ between base and head.
func (t Tree) ChangedPaths(ctx context.Context, base, head core.CommitSHA) ([]string, error) {
	return t.GitHub.ChangedPaths(ctx, t.Repo, base, head)
}

// NoScans is the scan source of a deployment without a trusted scanner:
// a change to the supply chain finds no scan and does not pass.
type NoScans struct{}

// Scan always answers NOT_FOUND.
func (NoScans) Scan(context.Context, qa.Candidate) (supplychain.ScanResult, error) {
	return supplychain.ScanResult{}, core.NewError(core.CodeNotFound, "no trusted vulnerability scanner is configured")
}

// NoDependencyApprovals is the approval source of a deployment where new
// dependencies are approved only by a new ADR: none is ever found.
type NoDependencyApprovals struct{}

// Approval always answers NOT_FOUND.
func (NoDependencyApprovals) Approval(context.Context, qa.Candidate, core.Digest) (approvals.Approval, error) {
	return approvals.Approval{}, core.NewError(core.CodeNotFound, "new dependencies need a new ADR approved by the Owner")
}

// reservationOf reads the reservation of a stored dispatcher run record.
func reservationOf(raw []byte) string {
	var run struct {
		ReservationID string `json:"reservation_id"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return ""
	}
	return run.ReservationID
}
