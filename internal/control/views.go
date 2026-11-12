package control

import (
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// HealthStatus is the verdict on one zone.
type HealthStatus string

const (
	// HealthOK means the zone answered its probe.
	HealthOK HealthStatus = "ok"
	// HealthDown means the probe failed or timed out.
	HealthDown HealthStatus = "down"
	// HealthUnknown means no probe is configured for the zone. It counts as
	// unhealthy: a zone nobody checks is not assumed to be fine.
	HealthUnknown HealthStatus = "unknown"
)

// ZoneHealth is the health of one zone. Detail is a short reason that is
// safe to show to the Owner; the unauthenticated health endpoint omits it.
type ZoneHealth struct {
	Zone   core.Zone    `json:"zone"`
	Status HealthStatus `json:"status"`
	Detail string       `json:"detail,omitempty"`
}

// StateCount is how many issues stand in one lifecycle state.
type StateCount struct {
	State core.State `json:"state"`
	Count int        `json:"count"`
}

// FactoryStatus is the overview of the factory. Its counts cover every state
// in lifecycle order, zero included, so a reader never has to guess whether a
// missing state means none or unknown.
type FactoryStatus struct {
	FactoryID core.FactoryID `json:"factory_id"`
	// Healthy is true only when all three zones answered.
	Healthy     bool         `json:"healthy"`
	Zones       []ZoneHealth `json:"zones"`
	IssueCounts []StateCount `json:"issue_counts"`
	IssueTotal  int          `json:"issue_total"`
	// PendingDeliveries counts webhook deliveries stored but not yet
	// processed, up to a fixed cap.
	PendingDeliveries int       `json:"pending_deliveries"`
	GeneratedAt       time.Time `json:"generated_at"`
}

// IssueSummary is one row of the workboard. Title and Labels come from
// GitHub and are untrusted text.
type IssueSummary struct {
	// ID is the issue reference in the form owner/repo#number.
	ID         string     `json:"id"`
	Owner      string     `json:"owner"`
	Repo       string     `json:"repo"`
	Number     int        `json:"number"`
	Title      string     `json:"title,omitempty"`
	State      core.State `json:"state"`
	Version    int64      `json:"version"`
	Attempt    int        `json:"attempt"`
	RunID      core.RunID `json:"run_id,omitempty"`
	LeaseOwner string     `json:"lease_owner,omitempty"`
	Labels     []string   `json:"labels,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DashboardPath is the dashboard page of the issue. The reference is escaped
// as one path segment, so slashes and the hash sign in it cannot alter the
// route.
func (s IssueSummary) DashboardPath() string { return "/issues/" + url.PathEscape(s.ID) }

// IssueList is the answer to a workboard query. Total counts every match;
// Issues holds at most the requested limit of them, newest change first.
type IssueList struct {
	Issues []IssueSummary `json:"issues"`
	Total  int            `json:"total"`
}

// IssueComment is a comment as last seen on a webhook. The body is untrusted.
type IssueComment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// IssueDetail is one issue with its operational record and recent comments.
type IssueDetail struct {
	IssueSummary
	FactoryID    core.FactoryID    `json:"factory_id"`
	FencingToken core.FencingToken `json:"fencing_token,omitempty"`
	Author       string            `json:"author,omitempty"`
	// GitHubState is "open" or "closed" as last reported by GitHub.
	GitHubState string         `json:"github_state,omitempty"`
	Comments    []IssueComment `json:"comments,omitempty"`
}
