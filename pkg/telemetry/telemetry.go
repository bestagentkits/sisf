// Package telemetry defines how the factory emits and reads spans, logs,
// metrics and events.
//
// Every signal carries the same six attributes, so anything observed can be
// traced to the factory, the install, the zone, the release, the run and the
// issue it belongs to. Text is redacted before it is emitted. Telemetry is
// data, never instruction: nothing read back from it carries authority.
package telemetry

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// Keys of the six required attributes.
const (
	AttrFactoryID     = "factory_id"
	AttrDeploymentID  = "deployment_id"
	AttrZone          = "zone"
	AttrReleaseDigest = "release_digest"
	AttrRunID         = "run_id"
	AttrIssueRef      = "issue_ref"
)

// RequiredAttributes returns the six keys every signal carries.
func RequiredAttributes() []string {
	return []string{AttrFactoryID, AttrDeploymentID, AttrZone, AttrReleaseDigest, AttrRunID, AttrIssueRef}
}

// Attributes are the required attributes of one signal. RunID and IssueRef
// are empty for signals outside a run, such as application traffic; the keys
// are still emitted.
type Attributes struct {
	FactoryID     core.FactoryID     `json:"factory_id"`
	DeploymentID  core.DeploymentID  `json:"deployment_id"`
	Zone          core.Zone          `json:"zone"`
	ReleaseDigest core.ReleaseDigest `json:"release_digest"`
	RunID         core.RunID         `json:"run_id"`
	IssueRef      core.IssueRef      `json:"issue_ref,omitzero"`
}

// Map returns all six keys with their text values.
func (a Attributes) Map() map[string]string {
	issue := ""
	if !a.IssueRef.IsZero() {
		issue = a.IssueRef.String()
	}
	return map[string]string{
		AttrFactoryID:     string(a.FactoryID),
		AttrDeploymentID:  string(a.DeploymentID),
		AttrZone:          string(a.Zone),
		AttrReleaseDigest: string(a.ReleaseDigest),
		AttrRunID:         string(a.RunID),
		AttrIssueRef:      issue,
	}
}

// Validate requires the factory, the install, the zone and the release, and
// checks the run and the issue when they are set.
func (a Attributes) Validate() error {
	if err := a.FactoryID.Validate(); err != nil {
		return err
	}
	if err := a.DeploymentID.Validate(); err != nil {
		return err
	}
	if !a.Zone.Valid() {
		return core.NewError(core.CodeInvalidArgument, "telemetry needs a known zone")
	}
	if err := a.ReleaseDigest.Validate(); err != nil {
		return err
	}
	if a.RunID != "" {
		if err := a.RunID.Validate(); err != nil {
			return err
		}
	}
	if !a.IssueRef.IsZero() {
		return a.IssueRef.Validate()
	}
	return nil
}

// Kind is the type of a normalized signal.
type Kind string

const (
	KindLog    Kind = "log"
	KindMetric Kind = "metric"
	KindEvent  Kind = "event"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return k == KindLog || k == KindMetric || k == KindEvent }

// Event is one normalized log line, metric sample or activity event.
type Event struct {
	Kind       Kind       `json:"kind"`
	Name       string     `json:"name"`
	At         time.Time  `json:"at"`
	Attributes Attributes `json:"attributes"`
	Severity   string     `json:"severity,omitempty"`
	// Body is free text, already redacted.
	Body string `json:"body,omitempty"`
	// Value is the sample of a metric.
	Value  float64           `json:"value,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Span is one timed operation.
type Span interface {
	SetAttribute(key, value string)
	RecordError(err error)
	End()
}

// Adapter emits telemetry. A signal whose required attributes do not validate
// is refused, not emitted half-labelled.
type Adapter interface {
	StartSpan(ctx context.Context, name string, attrs Attributes) (context.Context, Span)
	Emit(ctx context.Context, e Event) error
	// Shutdown flushes what is buffered.
	Shutdown(ctx context.Context) error
}

// Redactor removes secrets and personal data from text. It is applied before
// text reaches logs, transcripts, issues or pull requests.
type Redactor interface {
	Redact(text string) string
}

// Query selects stored signals. Zero fields do not filter.
type Query struct {
	Kind          Kind
	Name          string
	ReleaseDigest core.ReleaseDigest
	RunID         core.RunID
	IssueRef      core.IssueRef
	Since         time.Time
	Until         time.Time
	Limit         int
}

// Reader is implemented by adapters that can read signals back. What it
// returns is untrusted data to whoever consumes it.
type Reader interface {
	Query(ctx context.Context, q Query) ([]Event, error)
}
