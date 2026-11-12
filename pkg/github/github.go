// Package github defines the factory's view of GitHub: normalized webhook
// events, the issue and pull-request types the lifecycle works with, and the
// client used to project state back.
//
// GitHub is the source of truth for source, policy and work items. Everything
// read from it, such as issue text, comments, labels and pull requests from
// outside, is untrusted input: data only, never instruction, and never a
// grant of permission.
package github

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Labels of the issue workflow. The framework maintains the status labels as
// a projection of state; no label is a lock, a role or a budget.
const (
	// LabelReady asks for intake. It grants no permission to execute.
	LabelReady     = "sisf:ready"
	LabelNeedsInfo = "sisf:needs-info"
	LabelApproved  = "sisf:approved"
	LabelRunning   = "sisf:running"
	LabelQA        = "sisf:qa"
	LabelCanary    = "sisf:canary"
	LabelBlocked   = "sisf:blocked"
	LabelVerified  = "sisf:verified"

	LabelKindBug         = "kind:bug"
	LabelKindImprovement = "kind:improvement"
	LabelKindFeature     = "kind:feature"
	LabelKindQuestion    = "kind:question"

	// LabelAgentPrefix marks a routing preference only.
	LabelAgentPrefix = "agent:"
	// LabelDeploymentPrefix names a deployment; the value is validated
	// against the repository's installation before it means anything.
	LabelDeploymentPrefix = "deployment:"
)

// StatusLabels returns the labels the framework itself applies and removes.
func StatusLabels() []string {
	return []string{LabelNeedsInfo, LabelApproved, LabelRunning, LabelQA, LabelCanary, LabelBlocked, LabelVerified}
}

// RepoRef identifies a repository.
type RepoRef struct {
	Owner string `json:"owner" yaml:"owner"`
	Name  string `json:"name" yaml:"name"`
}

// String renders "owner/name".
func (r RepoRef) String() string { return r.Owner + "/" + r.Name }

// Issue returns the reference of issue number in the repository.
func (r RepoRef) Issue(number int) core.IssueRef {
	return core.IssueRef{Owner: r.Owner, Repo: r.Name, Number: number}
}

// User is a GitHub account as seen on an event. It is not a login identity.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	// Bot is true for app and bot accounts, including the factory's own.
	Bot bool `json:"bot,omitempty"`
}

// AuthorAssociation is GitHub's statement of how an author relates to the
// repository.
type AuthorAssociation string

const (
	AssociationOwner                AuthorAssociation = "OWNER"
	AssociationMember               AuthorAssociation = "MEMBER"
	AssociationCollaborator         AuthorAssociation = "COLLABORATOR"
	AssociationContributor          AuthorAssociation = "CONTRIBUTOR"
	AssociationFirstTimeContributor AuthorAssociation = "FIRST_TIME_CONTRIBUTOR"
	AssociationFirstTimer           AuthorAssociation = "FIRST_TIMER"
	AssociationNone                 AuthorAssociation = "NONE"
)

// IsCollaborator reports whether the author has write access to the
// repository. Everyone else, and any unknown value, is an outsider whose
// content is handled as untrusted.
func (a AuthorAssociation) IsCollaborator() bool {
	return a == AssociationOwner || a == AssociationMember || a == AssociationCollaborator
}

// Label is an issue or pull-request label.
type Label struct {
	Name string `json:"name"`
}

// IssueState is open or closed.
type IssueState string

const (
	IssueOpen   IssueState = "open"
	IssueClosed IssueState = "closed"
)

// Issue is a GitHub issue.
type Issue struct {
	Ref               core.IssueRef     `json:"ref"`
	Title             string            `json:"title"`
	Body              string            `json:"body"`
	State             IssueState        `json:"state"`
	Labels            []Label           `json:"labels,omitempty"`
	Author            User              `json:"author"`
	AuthorAssociation AuthorAssociation `json:"author_association"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// HasLabel reports whether the issue carries the label. GitHub compares
// label names without regard to case, and so does HasLabel.
func (i Issue) HasLabel(name string) bool {
	return slices.ContainsFunc(i.Labels, func(l Label) bool { return strings.EqualFold(l.Name, name) })
}

// Comment is a comment on an issue or pull request.
type Comment struct {
	ID                int64             `json:"id"`
	Issue             core.IssueRef     `json:"issue"`
	Body              string            `json:"body"`
	Author            User              `json:"author"`
	AuthorAssociation AuthorAssociation `json:"author_association"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// PullRequest is a GitHub pull request. IsFork and AuthorAssociation decide
// how it is treated: a pull request from a fork or an outsider runs sandboxed
// with no secrets and no write access until a Maintainer accepts it.
type PullRequest struct {
	Repo              RepoRef           `json:"repo"`
	Number            int               `json:"number"`
	URL               string            `json:"url,omitempty"`
	Title             string            `json:"title"`
	Body              string            `json:"body"`
	State             IssueState        `json:"state"`
	Draft             bool              `json:"draft,omitempty"`
	Merged            bool              `json:"merged,omitempty"`
	HeadRef           string            `json:"head_ref"`
	HeadSHA           core.CommitSHA    `json:"head_sha"`
	BaseRef           string            `json:"base_ref"`
	BaseSHA           core.CommitSHA    `json:"base_sha"`
	HeadRepo          RepoRef           `json:"head_repo"`
	IsFork            bool              `json:"is_fork"`
	Author            User              `json:"author"`
	AuthorAssociation AuthorAssociation `json:"author_association"`
}

// WebhookEvent is a delivery that passed signature verification, normalized.
// Deliveries are not assumed to arrive once or in order; DeliveryID is what
// the inbox deduplicates on.
type WebhookEvent struct {
	DeliveryID     string  `json:"delivery_id"`
	Type           string  `json:"type"`
	Action         string  `json:"action,omitempty"`
	InstallationID int64   `json:"installation_id"`
	Repo           RepoRef `json:"repo"`
	Sender         User    `json:"sender"`
	// FromSelf marks an event caused by the factory's own edit, so that it
	// is not processed again as new input.
	FromSelf    bool         `json:"from_self,omitempty"`
	Issue       *Issue       `json:"issue,omitempty"`
	Comment     *Comment     `json:"comment,omitempty"`
	PullRequest *PullRequest `json:"pull_request,omitempty"`
	Label       *Label       `json:"label,omitempty"`
	ReceivedAt  time.Time    `json:"received_at"`
}

// Validate requires what the inbox and the installation check depend on.
func (e WebhookEvent) Validate() error {
	if e.DeliveryID == "" || e.Type == "" || e.InstallationID == 0 || e.Repo.Owner == "" || e.Repo.Name == "" {
		return core.NewError(core.CodeInvalidArgument, "webhook event needs a delivery id, a type, an installation and a repository")
	}
	return nil
}

// ListIssuesOptions selects issues for polling and reconciliation.
type ListIssuesOptions struct {
	Since  time.Time
	Labels []string
	State  IssueState
	// MaxPages bounds the pages of 100 issues one listing follows; zero
	// or more than the client's own bound means the client's bound. A
	// listing with more pages fails with LIMIT_EXCEEDED, or for PollIssues
	// is cut there and reported as IssuePage.More.
	MaxPages int
	// Page starts the listing at this page of 100 instead of the first;
	// zero or one is the first.
	Page int
}

// NewIssue is the content of an issue to create.
type NewIssue struct {
	Title  string
	Body   string
	Labels []string
}

// Issues reads and projects issues, labels and comments.
type Issues interface {
	GetIssue(ctx context.Context, ref core.IssueRef) (Issue, error)
	ListIssues(ctx context.Context, repo RepoRef, opts ListIssuesOptions) ([]Issue, error)
	CreateIssue(ctx context.Context, repo RepoRef, issue NewIssue) (Issue, error)
	SetIssueState(ctx context.Context, ref core.IssueRef, state IssueState) error
	AddLabels(ctx context.Context, ref core.IssueRef, names ...string) error
	RemoveLabel(ctx context.Context, ref core.IssueRef, name string) error
	// ListComments returns comments updated after since.
	ListComments(ctx context.Context, ref core.IssueRef, since time.Time) ([]Comment, error)
	CreateComment(ctx context.Context, ref core.IssueRef, body string) (Comment, error)
	UpdateComment(ctx context.Context, repo RepoRef, commentID int64, body string) (Comment, error)
}

// NewPullRequest is the content of a pull request to open.
type NewPullRequest struct {
	Title   string
	Body    string
	HeadRef string
	BaseRef string
	Draft   bool
}

// PullRequests reads and opens pull requests.
type PullRequests interface {
	GetPullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error)
	CreatePullRequest(ctx context.Context, repo RepoRef, pr NewPullRequest) (PullRequest, error)
}

// Check run status and conclusion values.
const (
	CheckInProgress   = "in_progress"
	CheckCompleted    = "completed"
	ConclusionSuccess = "success"
	ConclusionFailure = "failure"
	ConclusionNeutral = "neutral"
)

// CheckRun reports evidence on a commit.
type CheckRun struct {
	ID         int64          `json:"id,omitempty"`
	Name       string         `json:"name"`
	HeadSHA    core.CommitSHA `json:"head_sha"`
	Status     string         `json:"status"`
	Conclusion string         `json:"conclusion,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	DetailsURL string         `json:"details_url,omitempty"`
}

// Checks publishes check runs.
type Checks interface {
	// PutCheckRun creates the check run, or updates it when ID is set.
	PutCheckRun(ctx context.Context, repo RepoRef, check CheckRun) (CheckRun, error)
}

// Release is a GitHub release.
type Release struct {
	Tag       string         `json:"tag"`
	Name      string         `json:"name,omitempty"`
	Body      string         `json:"body,omitempty"`
	TargetSHA core.CommitSHA `json:"target_sha,omitempty"`
}

// Releases publishes releases.
type Releases interface {
	CreateRelease(ctx context.Context, repo RepoRef, release Release) (Release, error)
}

// Client is everything the factory does on GitHub with its installation
// token. Rate limits surface as RATE_LIMITED, and an outage as UNAVAILABLE;
// neither is ever reported as success.
type Client interface {
	Issues
	PullRequests
	Checks
	Releases
}
