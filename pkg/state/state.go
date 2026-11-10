// Package state defines the contract of the operational store: the working
// state derived from GitHub that needs transactions GitHub cannot give
// (locks, jobs, ledger).
//
// Every write happens inside a transaction. State changes are compare-and-swap
// on a version, lease holders are ordered by fencing tokens, deliveries are
// accepted once, side effects leave through an outbox, and the budget ledger
// commits together with the claim it pays for. Consumers depend on the narrow
// role interfaces; Tx is their union.
package state

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Store is the operational store of one factory; every record it creates
// carries that factory's ID. Each zone opens it with its own role, so a zone
// can only reach the tables it was granted.
type Store interface {
	// Update runs fn in one read-write transaction. It commits only when fn
	// returns nil; any error rolls back every write fn made.
	Update(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// View runs fn in one read-only transaction.
	View(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	Close() error
}

// Tx is one transaction. Everything done through it is atomic.
type Tx interface {
	Records
	Leases
	Inbox
	Outbox
	core.BudgetLedger
	KV
}

// RecordFilter selects issue records. Zero fields do not filter.
type RecordFilter struct {
	States       []core.State
	UpdatedSince time.Time
	Limit        int
}

// Records stores the lifecycle record of each issue.
type Records interface {
	// GetRecord returns NOT_FOUND when the issue has no record.
	GetRecord(ctx context.Context, issue core.IssueRef) (core.StateRecord, error)
	ListRecords(ctx context.Context, filter RecordFilter) ([]core.StateRecord, error)
	// Transition applies t only if the stored version equals
	// t.ExpectedVersion and the stored state equals t.From; otherwise it
	// returns CONFLICT and writes nothing. A non-zero t.FencingToken must be
	// the latest token issued for IssueLeaseResource(t.IssueID), else
	// STALE_FENCING_TOKEN. A repeated t.IdempotencyKey returns the record
	// written the first time without writing again. Whether the edge is
	// legal is decided by the transition table before this call.
	Transition(ctx context.Context, t core.Transition) (core.StateRecord, error)
}

// IssueLeaseResource is the lease resource that guards writes to one issue.
func IssueLeaseResource(issue core.IssueRef) string { return "issue:" + issue.String() }

// Lease is exclusive ownership of a resource for a limited time.
type Lease struct {
	Resource  string            `json:"resource"`
	Owner     string            `json:"owner"`
	Token     core.FencingToken `json:"token"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Expired reports whether the lease is no longer held at now.
func (l Lease) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

// Leases grants exclusive, expiring ownership with fencing.
type Leases interface {
	// AcquireLease grants the resource to owner when it is free or its lease
	// has expired, with a token strictly greater than any issued before for
	// the resource. It returns LEASE_HELD while another owner holds it.
	AcquireLease(ctx context.Context, resource, owner string, ttl time.Duration) (Lease, error)
	// RenewLease extends a lease its holder still owns and keeps its token.
	// It returns STALE_FENCING_TOKEN once a newer holder exists.
	RenewLease(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error)
	// ReleaseLease gives the resource up early. Tokens are never reused.
	ReleaseLease(ctx context.Context, lease Lease) error
	// CheckFencing returns STALE_FENCING_TOKEN unless token is the latest one
	// issued for resource. Every report from a lease holder passes this check.
	CheckFencing(ctx context.Context, resource string, token core.FencingToken) error
}

// InboxEntry is one delivery received from outside, stored before it is
// processed. Deliveries may arrive late, twice or out of order.
type InboxEntry struct {
	// Source and DeliveryID together identify the delivery.
	Source      string    `json:"source"`
	DeliveryID  string    `json:"delivery_id"`
	EventType   string    `json:"event_type"`
	Payload     []byte    `json:"payload"`
	ReceivedAt  time.Time `json:"received_at"`
	ProcessedAt time.Time `json:"processed_at,omitzero"`
}

// Inbox accepts each delivery exactly once.
type Inbox interface {
	// AcceptDelivery stores the entry and returns true, or returns false
	// without storing when the same source and delivery ID were seen before.
	AcceptDelivery(ctx context.Context, e InboxEntry) (accepted bool, err error)
	// PendingDeliveries returns unprocessed entries, oldest first.
	PendingDeliveries(ctx context.Context, limit int) ([]InboxEntry, error)
	MarkDeliveryProcessed(ctx context.Context, source, deliveryID string) error
}

// OutboxMessage is one side effect to perform outside the store, such as a
// comment or a label on GitHub. It is written in the transaction that decided
// it, so a decision and its side effect cannot diverge.
type OutboxMessage struct {
	ID    int64  `json:"id"`
	Topic string `json:"topic"`
	// Key makes the effect idempotent: one message exists per key.
	Key           string    `json:"key"`
	Payload       []byte    `json:"payload"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	SentAt        time.Time `json:"sent_at,omitzero"`
}

// Outbox queues side effects for delivery after commit.
type Outbox interface {
	// Enqueue adds the message; a key that is already queued or sent is a no-op.
	Enqueue(ctx context.Context, m OutboxMessage) error
	// PendingOutbox returns unsent messages that are due, oldest first.
	PendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error)
	MarkOutboxSent(ctx context.Context, id int64) error
	// MarkOutboxFailed records the failure and schedules the next attempt.
	MarkOutboxFailed(ctx context.Context, id int64, retryAt time.Time, reason string) error
}

// KVEntry is one versioned document in a namespace.
type KVEntry struct {
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	Value     []byte    `json:"value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// KV stores versioned documents for components whose data has no table of
// its own. It is transactional with everything else in Tx.
type KV interface {
	// GetKV returns NOT_FOUND when the entry does not exist.
	GetKV(ctx context.Context, namespace, key string) (KVEntry, error)
	// PutKV writes the entry only if e.Version equals the stored version
	// (0 creates) and returns it with the new version; otherwise CONFLICT.
	PutKV(ctx context.Context, e KVEntry) (KVEntry, error)
	// ListKV returns entries whose key starts with prefix, in key order.
	ListKV(ctx context.Context, namespace, prefix string, limit int) ([]KVEntry, error)
	// DeleteKV removes the entry only if version equals the stored version.
	DeleteKV(ctx context.Context, namespace, key string, version int64) error
}

// Migration describes one schema change. BackwardCompatible says the previous
// release still works on the new schema; only such migrations may ride along
// with a self-published release, because rollback restores code, not data.
type Migration struct {
	Version            int    `json:"version"`
	Name               string `json:"name"`
	BackwardCompatible bool   `json:"backward_compatible"`
}

// ValidateMigrations requires versions 1, 2, 3, ... in order and a name each.
func ValidateMigrations(ms []Migration) error {
	for i, m := range ms {
		if m.Version != i+1 || m.Name == "" {
			return core.NewError(core.CodeInvalidArgument, "migrations must be named and numbered from 1 without gaps")
		}
	}
	return nil
}

// Incompatible returns the migrations that are not backward compatible.
func Incompatible(ms []Migration) []Migration {
	var out []Migration
	for _, m := range ms {
		if !m.BackwardCompatible {
			out = append(out, m)
		}
	}
	return out
}

// MigrateOptions controls a migration run.
type MigrateOptions struct {
	// AllowIncompatible is set only after the Owner approved a migration that
	// is not backward compatible.
	AllowIncompatible bool
}

// Migrator applies versioned schema migrations.
type Migrator interface {
	SchemaVersion(ctx context.Context) (int, error)
	Pending(ctx context.Context) ([]Migration, error)
	// Migrate applies pending migrations in order. It returns
	// APPROVAL_REQUIRED and applies nothing when a pending migration is not
	// backward compatible and opts does not allow it.
	Migrate(ctx context.Context, opts MigrateOptions) error
}
