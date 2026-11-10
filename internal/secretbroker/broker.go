// Package secretbroker is the per-run secret broker: the only path by which
// a run obtains a secret.
//
// A grant lets one run use one secret for one purpose, within named scopes,
// until it expires or the run is revoked. The broker trusts the provider's
// registry, not the request, for what a secret is: a reference the provider
// lists as a control-plane credential or a signing key (the GitHub App key,
// Owner tokens, webhook secrets, delegated keys), or as owned by another
// zone than the agent zone, is refused however the request labels it.
// Values are resolved at redemption, never cached and never logged.
//
// Grants live in memory. A restart of the broker therefore revokes every
// grant, which is the safe direction.
package secretbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// DefaultMaxTTL caps the lifetime of a grant when the configuration sets none.
const DefaultMaxTTL = time.Hour

// Limits on a request's purpose and scopes.
const (
	maxScopes = 16
	maxGrants = 64 // per run
)

// PurposeSandboxEnv is the purpose of a secret a sandbox receives as an
// environment variable. Such a grant names the variable in exactly one
// scope made with EnvScope.
const PurposeSandboxEnv = "sandbox.env"

var (
	purposePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	scopePattern   = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)
	envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// EnvScope is the scope that names the environment variable a sandbox
// receives a secret in.
func EnvScope(name string) string { return "env:" + name }

// EnvName returns the variable a sandbox-env grant names, or "" when the
// grant names none or more than one.
func EnvName(g secrets.Grant) string {
	if g.Purpose != PurposeSandboxEnv {
		return ""
	}
	name := ""
	for _, scope := range g.Scopes {
		if v, ok := strings.CutPrefix(scope, "env:"); ok {
			if name != "" || !envNamePattern.MatchString(v) {
				return ""
			}
			name = v
		}
	}
	return name
}

// Config is what a broker is built from.
type Config struct {
	// Provider resolves references to values and lists what each is.
	Provider secrets.SecretProvider
	// Clock is the source of time; the wall clock when nil.
	Clock core.Clock
	// MaxTTL caps every grant's lifetime; DefaultMaxTTL when zero.
	MaxTTL time.Duration
	// Audit, when set, receives one record per issue, redemption, refusal
	// and revocation, naming references only. FactoryID is then required.
	Audit     core.AuditLog
	FactoryID core.FactoryID
}

// Broker implements secrets.Broker. It is safe for concurrent use.
type Broker struct {
	cfg Config

	mu      sync.Mutex
	grants  map[string]*grant
	revoked map[core.RunID]bool
}

type grant struct {
	secrets.Grant
	revoked bool
}

var _ secrets.Broker = (*Broker)(nil)

// New builds a broker, refusing a configuration without a provider.
func New(cfg Config) (*Broker, error) {
	switch {
	case cfg.Provider == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the secret broker needs a secret provider")
	case cfg.MaxTTL < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the grant lifetime cap must not be negative")
	}
	if cfg.Audit != nil {
		if err := cfg.FactoryID.Validate(); err != nil {
			return nil, err
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.MaxTTL == 0 {
		cfg.MaxTTL = DefaultMaxTTL
	}
	return &Broker{cfg: cfg, grants: map[string]*grant{}, revoked: map[core.RunID]bool{}}, nil
}

// Issue grants one run the use of one secret. Besides the contract's checks
// it refuses, with PERMISSION_DENIED, a reference the provider does not list
// as a run secret of the agent zone, a request that misstates the class or
// the zone, and any request for a run that was revoked. Lifetimes beyond
// the cap are cut to it.
func (b *Broker) Issue(ctx context.Context, req secrets.GrantRequest) (secrets.Grant, error) {
	g, err := b.issue(ctx, req)
	b.audit(ctx, "secrets.grant.issue", req.RunID, req.Ref, err)
	return g, err
}

func (b *Broker) issue(ctx context.Context, req secrets.GrantRequest) (secrets.Grant, error) {
	if err := req.Validate(); err != nil {
		return secrets.Grant{}, err
	}
	if err := checkPurpose(req.Purpose, req.Scopes); err != nil {
		return secrets.Grant{}, err
	}
	if req.Ref.Zone != core.ZoneAgent {
		return secrets.Grant{}, denied("only secrets of the agent zone may be granted to a run")
	}
	if err := b.registered(ctx, req.Ref); err != nil {
		return secrets.Grant{}, err
	}
	id, err := newID()
	if err != nil {
		return secrets.Grant{}, err
	}
	now := b.cfg.Clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.revoked[req.RunID] {
		return secrets.Grant{}, denied("the run was revoked; it gets no more secrets")
	}
	live := 0
	for id, g := range b.grants {
		switch {
		case g.Expired(now):
			delete(b.grants, id)
		case g.RunID == req.RunID:
			live++
		}
	}
	if live >= maxGrants {
		return secrets.Grant{}, core.NewError(core.CodeLimitExceeded, "the run holds too many grants")
	}
	g := secrets.Grant{ID: id, RunID: req.RunID, Ref: req.Ref, Purpose: req.Purpose,
		Scopes: slices.Clone(req.Scopes), ExpiresAt: now.Add(min(req.TTL, b.cfg.MaxTTL))}
	b.grants[id] = &grant{Grant: g}
	return g, nil
}

func checkPurpose(purpose string, scopes []string) error {
	if !purposePattern.MatchString(purpose) {
		return core.NewError(core.CodeInvalidArgument, "a grant's purpose must be a short lowercase name")
	}
	if len(scopes) > maxScopes {
		return core.NewError(core.CodeInvalidArgument, "a grant has too many scopes")
	}
	for _, s := range scopes {
		if !scopePattern.MatchString(s) {
			return core.NewError(core.CodeInvalidArgument, "a grant scope must be a short name without spaces")
		}
	}
	if purpose == PurposeSandboxEnv && EnvName(secrets.Grant{Purpose: purpose, Scopes: scopes}) == "" {
		return core.NewError(core.CodeInvalidArgument, "a sandbox environment grant names exactly one upper-case variable in an env: scope")
	}
	return nil
}

// registered checks the reference against the provider's own registry:
// the provider, not the request, says what a secret is.
func (b *Broker) registered(ctx context.Context, ref secrets.SecretRef) error {
	refs, err := b.cfg.Provider.ListRefs(ctx)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the secret provider could not list its secrets").WithCause(err)
	}
	for _, known := range refs {
		if known.Provider != ref.Provider || known.Name != ref.Name {
			continue
		}
		switch {
		case !known.Class.BrokerableToRun() || known.Zone != core.ZoneAgent:
			return denied("secret " + ref.String() + " may not be granted to a run")
		case known.Class != ref.Class || known.Zone != ref.Zone:
			return denied("the request misstates the class or the zone of secret " + ref.String())
		}
		return nil
	}
	return core.NewError(core.CodeNotFound, "secret "+ref.String()+" is not known to its provider")
}

func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", core.NewError(core.CodeInternal, "no randomness for a grant ID").WithCause(err)
	}
	return "grant-" + hex.EncodeToString(raw), nil
}

func denied(message string) error { return core.NewError(core.CodePermissionDenied, message) }
