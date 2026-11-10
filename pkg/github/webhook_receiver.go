package github

import (
	"context"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Header names GitHub sets on a delivery.
const (
	EventHeader    = "X-GitHub-Event"
	DeliveryHeader = "X-GitHub-Delivery"
)

var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Verify checks a delivery and normalizes it. It does the checks in the
// order that exposes the least: the signature over the raw bytes first, so
// nothing about an unauthenticated body is parsed or answered; then the
// headers; then the installation, repository and action.
//
// The returned error carries a code the HTTP layer maps to a status:
// UNAUTHENTICATED and SIGNATURE_INVALID, INVALID_ARGUMENT, PERMISSION_DENIED
// for a foreign installation or repository, and NOT_SUPPORTED for a verified
// delivery the factory does not react to.
func (r *Receiver) Verify(signature, eventType, deliveryID string, body []byte) (WebhookEvent, error) {
	if err := verifySignature(signature, body, r.cfg.Secrets()); err != nil {
		return WebhookEvent{}, err
	}
	if !deliveryIDPattern.MatchString(deliveryID) {
		return WebhookEvent{}, core.NewError(core.CodeInvalidArgument, "the delivery id is missing or malformed")
	}
	if eventType == "" {
		return WebhookEvent{}, core.NewError(core.CodeInvalidArgument, "the delivery names no event type")
	}
	if !handlesType(eventType) {
		return WebhookEvent{}, core.NewError(core.CodeNotSupported, "the factory does not react to this event type")
	}
	ev, err := decodePayload(deliveryID, eventType, body, r.cfg.Clock.Now())
	if err != nil {
		return WebhookEvent{}, err
	}
	if err := ev.Validate(); err != nil {
		return WebhookEvent{}, err
	}
	if ev.InstallationID != r.cfg.InstallationID {
		return WebhookEvent{}, core.NewError(core.CodePermissionDenied, "the delivery belongs to another installation")
	}
	if !r.repoAllowed(ev.Repo) {
		return WebhookEvent{}, core.NewError(core.CodePermissionDenied, "the delivery concerns a repository this factory does not serve")
	}
	if !handles(ev.Type, ev.Action) {
		return WebhookEvent{}, core.NewError(core.CodeNotSupported, "the factory does not react to this action")
	}
	ev.FromSelf = r.cfg.Self.Caused(ev)
	return ev, nil
}

// repoAllowed reports whether the repository is one this factory serves. An
// empty list serves none.
func (r *Receiver) repoAllowed(repo RepoRef) bool {
	for _, allowed := range r.cfg.Repos {
		if sameRepo(allowed, repo) {
			return true
		}
	}
	return false
}

// Sink receives an accepted event. It should persist the event to the inbox
// and return; processing happens later. A returned error makes the receiver
// answer 5xx so the delivery can be retried or redelivered.
type Sink func(ctx context.Context, ev WebhookEvent) error

// ReceiverConfig describes which deliveries the receiver accepts.
type ReceiverConfig struct {
	// Secrets returns the active webhook secrets. It is called for every
	// delivery, so a rotated secret takes effect without a restart; during
	// rotation it returns both the old and the new secret.
	Secrets func() []core.SecretValue
	// InstallationID is the only installation whose deliveries are accepted.
	InstallationID int64
	// Repos lists the repositories this factory serves.
	Repos []RepoRef
	// Self is the factory's own account, used to mark echoes of its edits.
	Self Identity
	// Clock stamps ReceivedAt. It defaults to the system clock.
	Clock core.Clock
	// MaxBodyBytes caps the request body. It defaults to 25 MiB, the
	// largest payload GitHub sends.
	MaxBodyBytes int64
	// SeenCapacity bounds the memory of handled delivery ids (default 4096).
	SeenCapacity int
}

// Receiver verifies and normalizes webhook deliveries and hands them to a
// Sink. It is an http.Handler.
type Receiver struct {
	cfg    ReceiverConfig
	sink   Sink
	ledger *deliveryLedger
}

// NewReceiver validates the configuration. It fails closed: a receiver
// without secrets, an installation, a repository or a Self identity could not
// make its checks, so it is not built.
func NewReceiver(cfg ReceiverConfig, sink Sink) (*Receiver, error) {
	switch {
	case cfg.Secrets == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "a webhook receiver needs a secret source")
	case cfg.InstallationID == 0:
		return nil, core.NewError(core.CodeInvalidArgument, "a webhook receiver needs an installation id")
	case len(cfg.Repos) == 0:
		return nil, core.NewError(core.CodeInvalidArgument, "a webhook receiver needs at least one repository")
	case cfg.Self.IsZero():
		return nil, core.NewError(core.CodeInvalidArgument, "a webhook receiver needs the factory's own account to guard against loops")
	case sink == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "a webhook receiver needs a sink")
	}
	for _, repo := range cfg.Repos {
		if strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
			return nil, core.NewError(core.CodeInvalidArgument, "a served repository needs an owner and a name")
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 25 << 20
	}
	if cfg.SeenCapacity <= 0 {
		cfg.SeenCapacity = 4096
	}
	return &Receiver{cfg: cfg, sink: sink, ledger: newDeliveryLedger(cfg.SeenCapacity)}, nil
}
