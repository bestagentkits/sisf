// Package audit is the factory's audit log: an append-only chain of records
// in which each record carries the hash of the one before it.
//
// A record's hash covers its whole content, its position and the previous
// hash, so editing a record, removing one or changing their order breaks the
// chain at that point and Verify reports it. The head of the chain is kept
// apart from the records, so cutting records off the end is detected too.
// The log has no operation that changes or removes a record, for any role,
// and the database rejects such statements on its own.
//
// What the chain cannot detect by itself is someone with full control of the
// database who rewrites every record from some point on together with the
// head. Guarding against that needs a copy of the head kept outside the
// factory.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// hashDomain separates audit hashes from any other use of SHA-256 over
// similar bytes and names the format, so it can change without ambiguity.
const hashDomain = "sisf.audit.v1\n"

// Log is the audit log of one factory, kept in the factory's state store.
// It is safe for concurrent use, also by several processes on one database.
type Log struct {
	store *sqlstore.Store
	clock core.Clock
}

var (
	_ core.AuditLog    = (*Log)(nil)
	_ core.AuditReader = (*Log)(nil)
)

// New returns the audit log kept in store. The log belongs to the factory
// the store was opened for and is limited by the store's zone. Clock stamps
// events that carry no time; nil means the wall clock.
func New(store *sqlstore.Store, clock core.Clock) *Log {
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &Log{store: store, clock: clock}
}

// Append writes the event at the end of the chain and returns it as stored.
// Seq, PrevHash and Hash are assigned here; values the caller put there are
// ignored. An event without a time gets the current one, and an event
// without a factory gets the factory of the log.
//
// Append is a transaction of its own. It is refused when called from inside
// a state store transaction on the same store: write the audit record
// before or after that transaction, not within it.
func (l *Log) Append(ctx context.Context, e core.AuditEvent) (core.AuditEvent, error) {
	if e.FactoryID == "" {
		e.FactoryID = l.store.FactoryID()
	}
	if err := l.validate(e); err != nil {
		return core.AuditEvent{}, err
	}
	if e.At.IsZero() {
		e.At = l.clock.Now()
	}
	// UTC without a monotonic reading encodes the same on every host.
	e.At = e.At.UTC().Round(0)

	_, err := l.store.AppendAuditRow(ctx, func(seq int64, prevHash string) (string, []byte, error) {
		e.Seq, e.PrevHash = seq, prevHash
		hash, body, err := seal(e)
		e.Hash = hash
		return hash, body, err
	})
	if err != nil {
		return core.AuditEvent{}, err
	}
	return e, nil
}

// Events returns up to limit records with Seq greater than afterSeq, in
// order; a limit of zero or less returns them all. Each record is checked
// against its own hash before it is returned, so a record that was edited
// is reported as TAMPERED instead of being shown as if it were true.
// Whether the chain as a whole is intact is what Verify tells.
func (l *Log) Events(ctx context.Context, afterSeq int64, limit int) ([]core.AuditEvent, error) {
	var events []core.AuditEvent
	err := l.store.ViewAudit(ctx, func(view *sqlstore.AuditView) error {
		rows, err := view.Rows(ctx, afterSeq, limit)
		if err != nil {
			return err
		}
		events = make([]core.AuditEvent, 0, len(rows))
		for _, row := range rows {
			event, err := l.open(row)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// validate checks the event at the boundary of the log: who, what and the
// outcome must be stated, and every reference that is present must be whole.
func (l *Log) validate(e core.AuditEvent) error {
	switch {
	case e.FactoryID != l.store.FactoryID():
		return invalid("factory %q is not the factory of this audit log", string(e.FactoryID))
	case e.ActorID == "":
		return invalid("an audit event names its actor")
	case !e.ActorKind.Valid():
		return invalid("unknown actor kind %q", string(e.ActorKind))
	case e.Action == "":
		return invalid("an audit event names its action")
	case !e.Outcome.Valid():
		return invalid("unknown audit outcome %q", string(e.Outcome))
	case e.Surface != "" && !e.Surface.Valid():
		return invalid("unknown surface %q", string(e.Surface))
	}
	if !e.IssueID.IsZero() {
		if err := e.IssueID.Validate(); err != nil {
			return err
		}
	}
	if e.SubjectDigest != "" {
		if err := e.SubjectDigest.Validate(); err != nil {
			return err
		}
	}
	if e.PolicySHA != "" {
		if err := e.PolicySHA.Validate(); err != nil {
			return err
		}
	}
	if e.RunID != "" {
		return e.RunID.Validate()
	}
	return nil
}

// seal encodes the event as it is stored and returns the hash of that
// encoding. The hash field itself is left out of what is hashed. Because
// the stored bytes are what the hash covers, a record keeps verifying even
// if the event type gains fields later.
func seal(e core.AuditEvent) (hash string, body []byte, err error) {
	e.Hash = ""
	body, err = json.Marshal(e)
	if err != nil {
		return "", nil, invalid("the audit event cannot be encoded")
	}
	return hashOf(body), body, nil
}

// hashOf returns the chain hash of a stored body.
func hashOf(body []byte) string {
	h := sha256.New()
	h.Write([]byte(hashDomain))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// open decodes a stored row after checking that it is the record its hash
// and its place in the chain say it is.
func (l *Log) open(row sqlstore.AuditRow) (core.AuditEvent, error) {
	if hashOf(row.Body) != row.Hash {
		return core.AuditEvent{}, tampered(row.Seq, "its content does not match its hash")
	}
	var e core.AuditEvent
	if err := json.Unmarshal(row.Body, &e); err != nil {
		return core.AuditEvent{}, tampered(row.Seq, "its content cannot be read")
	}
	if e.Seq != row.Seq || e.PrevHash != row.PrevHash || e.FactoryID != l.store.FactoryID() {
		return core.AuditEvent{}, tampered(row.Seq, "it was written for another place in the chain")
	}
	e.Hash = row.Hash
	return e, nil
}

// tampered reports where the chain is broken. The sequence number is in the
// details so that an operator, or an agent, can go straight to the record.
func tampered(seq int64, reason string) *core.Error {
	refusal := core.NewError(core.CodeTampered, fmt.Sprintf("audit record %d was tampered with: %s", seq, reason))
	refusal.Details = map[string]string{"seq": strconv.FormatInt(seq, 10)}
	return refusal
}

func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}
