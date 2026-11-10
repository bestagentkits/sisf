// Package wiring assembles the factory's units from their parts: the state
// store, the GitHub adapter, the policy in force, intake, approvals, the
// budget gate and the dispatcher. The factoryd binary builds its two roles
// with it from the environment, and the in-process test harness builds the
// same roles from fakes, so what the tests prove is the wiring that runs.
package wiring

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The runtime contract of factoryd. The installer writes the secrets into the
// env file of the zone that runs the unit; nothing else carries them.
const (
	EnvConfig           = "SISF_CONFIG"
	EnvFactoryID        = "SISF_FACTORY_ID"
	EnvListen           = "SISF_LISTEN"
	EnvAllowNonLoopback = "SISF_LISTEN_ALLOW_NON_LOOPBACK"
	EnvStateDSN         = "SISF_STATE_DSN"
	EnvOwnerToken       = "SISF_OWNER_TOKEN"
	EnvWebhookSecret    = "SISF_WEBHOOK_SECRET"
	EnvGitHubToken      = "SISF_GITHUB_TOKEN"
	EnvGitHubAPIURL     = "SISF_GITHUB_API_URL"
	// EnvApprovalKey is the delegated key the approval store signs decisions
	// with: an ed25519 seed in unpadded base64url. Only the control zone has it.
	EnvApprovalKey = "SISF_APPROVAL_KEY"
	// EnvTrustedProxies lists, comma separated, the networks the edge
	// connects to the control zone from; the public report route trusts the
	// client address the edge forwards only from them. Loopback when unset.
	EnvTrustedProxies = "SISF_TRUSTED_PROXIES"
	// EnvAppHealthURL and EnvDispatcherHealthURL are where the control plane
	// probes Zone 1 and Zone 2. They default to the loopback ports of a
	// Standalone install.
	EnvAppHealthURL        = "SISF_APP_HEALTH_URL"
	EnvDispatcherHealthURL = "SISF_DISPATCHER_HEALTH_URL"
)

// Default addresses of a Standalone install; every port is on loopback.
const (
	DefaultControlListen       = "127.0.0.1:7463"
	DefaultDispatcherListen    = "127.0.0.1:7465"
	DefaultAppHealthURL        = "http://127.0.0.1:7460/healthz"
	DefaultDispatcherHealthURL = "http://127.0.0.1:7465/healthz"
)

// Env is the environment of one factoryd process, read once at start.
// Secret values stay wrapped so a careless print shows a marker.
type Env struct {
	Config              string
	FactoryID           core.FactoryID
	Listen              string
	AllowNonLoopback    bool
	StateDSN            core.SecretValue
	OwnerToken          core.SecretValue
	WebhookSecret       core.SecretValue
	GitHubToken         core.SecretValue
	GitHubAPIURL        string
	ApprovalKey         core.SecretValue
	AppHealthURL        string
	DispatcherHealthURL string
	TrustedProxies      []netip.Prefix
}

// ReadEnv reads the contract from getenv. It checks only the shape of what
// is there; each role says what it requires.
func ReadEnv(getenv func(string) string) (Env, error) {
	e := Env{
		Config:              strings.TrimSpace(getenv(EnvConfig)),
		FactoryID:           core.FactoryID(strings.TrimSpace(getenv(EnvFactoryID))),
		Listen:              strings.TrimSpace(getenv(EnvListen)),
		StateDSN:            secret(getenv(EnvStateDSN)),
		OwnerToken:          secret(getenv(EnvOwnerToken)),
		WebhookSecret:       secret(getenv(EnvWebhookSecret)),
		GitHubToken:         secret(getenv(EnvGitHubToken)),
		GitHubAPIURL:        strings.TrimSpace(getenv(EnvGitHubAPIURL)),
		ApprovalKey:         secret(getenv(EnvApprovalKey)),
		AppHealthURL:        strings.TrimSpace(getenv(EnvAppHealthURL)),
		DispatcherHealthURL: strings.TrimSpace(getenv(EnvDispatcherHealthURL)),
	}
	if raw := strings.TrimSpace(getenv(EnvAllowNonLoopback)); raw != "" {
		allow, err := strconv.ParseBool(raw)
		if err != nil {
			return e, core.NewError(core.CodeInvalidArgument, EnvAllowNonLoopback+" must be true or false")
		}
		e.AllowNonLoopback = allow
	}
	for _, raw := range strings.Split(getenv(EnvTrustedProxies), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return e, core.NewError(core.CodeInvalidArgument, EnvTrustedProxies+" must list networks such as 172.18.0.0/16")
		}
		e.TrustedProxies = append(e.TrustedProxies, prefix.Masked())
	}
	if e.Config == "" {
		return e, missing(EnvConfig)
	}
	if e.StateDSN.IsZero() {
		return e, missing(EnvStateDSN)
	}
	if e.FactoryID != "" {
		if err := e.FactoryID.Validate(); err != nil {
			return e, err
		}
	}
	return e, nil
}

// secret wraps a value read from the environment, dropping surrounding space.
func secret(raw string) core.SecretValue {
	return core.NewSecretValue([]byte(strings.TrimSpace(raw)))
}

// missing reports a variable the role cannot run without, by name only.
func missing(name string) error {
	return core.NewError(core.CodeInvalidArgument, name+" is not set")
}

// ParseApprovalKey decodes the approval signing key. The error never
// carries the value.
func ParseApprovalKey(value core.SecretValue) (ed25519.PrivateKey, error) {
	if value.IsZero() {
		return nil, missing(EnvApprovalKey)
	}
	seed, err := base64.RawURLEncoding.DecodeString(string(value.Reveal()))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, core.NewError(core.CodeInvalidArgument, EnvApprovalKey+" must be a 32-byte ed25519 seed in unpadded base64url")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// orDefault returns value, or fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// Pass intervals of the long-running roles. They bound how long a webhook,
// an effect or a Ready issue waits; each pass is a few indexed reads.
const (
	ControlPassInterval    = time.Second
	DispatcherPollInterval = 2 * time.Second
	DispatcherLeaseTTL     = 30 * time.Second
)
