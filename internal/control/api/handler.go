package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Paths served by the handler. MountPath is where the wiring package mounts
// it; every other path lies beneath it.
const (
	MountPath         = "/api/v1/"
	ActionPathPrefix  = "/api/v1/actions/"
	DeviceStartPath   = "/api/v1/device/start"
	DeviceTokenPath   = "/api/v1/device/token"
	DeviceVerifyPath  = "/api/v1/device/verify"
	DeviceApprovePath = "/api/v1/device/approve"
)

// Defaults for the Config fields left at zero.
const (
	DefaultMaxBodyBytes   = 1 << 20
	DefaultDeviceCodeTTL  = 10 * time.Minute
	DefaultDeviceTokenTTL = time.Hour
	DefaultPollInterval   = 5 * time.Second
)

// Actions is the action service; *actions.Service implements it.
type Actions interface {
	Invoke(ctx context.Context, name string, req core.ActionRequest) (core.ActionResult, error)
	Specs() []core.ActionSpec
}

// Identity is the authentication service; *auth.Service implements it.
type Identity interface {
	Middleware(surface core.Surface, next http.Handler) http.Handler
	// Handler serves the /auth/ routes; the verification page reads the
	// session view from it.
	Handler() http.Handler
	IssuePAT(actor core.Actor, req auth.PATRequest) (auth.IssuedToken, error)
}

// Config builds the REST handler.
type Config struct {
	Actions  Actions
	Identity Identity
	// Clock is the source of time for device codes; required.
	Clock core.Clock
	// ExternalURL is the public base URL of the control server, used in the
	// verification URL. Empty means the scheme and host of the request.
	ExternalURL string
	// LoginProviders are the provider names the verification page offers for
	// signing in and for step-up.
	LoginProviders []string
	// FormOrigins are the sign-in provider origins the verification page's
	// step-up form is redirected to. They join 'self' in the page's
	// form-action directive; pass the ones the dashboard uses.
	FormOrigins []string
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64
	// DeviceCodeTTL is how long a login request stays open.
	DeviceCodeTTL time.Duration
	// DeviceTokenTTL is the lifetime of the token a login request yields.
	DeviceTokenTTL time.Duration
	// PollInterval is the least time between polls of one login request.
	PollInterval time.Duration
	// Logger receives the causes of internal failures; nil discards them.
	Logger *slog.Logger
	// ClientKey names the network source of a request: when every login
	// request slot is taken, the waiting requests of the source holding the
	// most go first, and a new one is refused when no source holds more than
	// one. control.ClientAddress builds one that names an IPv6 client by its
	// /64. Nil means the remote IP address.
	ClientKey func(*http.Request) string
}

type handler struct {
	cfg     Config
	devices *deviceStore
	scopes  []string
}

// New returns the REST handler. Mount it at MountPath without stripping the
// prefix.
func New(cfg Config) (http.Handler, error) {
	switch {
	case cfg.Actions == nil || cfg.Identity == nil || cfg.Clock == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the REST handler needs the action service, the authentication service and a clock")
	case cfg.MaxBodyBytes < 0 || cfg.DeviceCodeTTL < 0 || cfg.DeviceTokenTTL < 0 || cfg.PollInterval < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "limits and durations must not be negative")
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.DeviceCodeTTL == 0 {
		cfg.DeviceCodeTTL = DefaultDeviceCodeTTL
	}
	if cfg.DeviceTokenTTL == 0 {
		cfg.DeviceTokenTTL = DefaultDeviceTokenTTL
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if u := strings.TrimRight(cfg.ExternalURL, "/"); u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, core.NewError(core.CodeInvalidArgument, "the external URL must start with http:// or https://")
	}
	h := &handler{cfg: cfg, devices: newDeviceStore(cfg.Clock, cfg.DeviceCodeTTL, cfg.PollInterval)}
	h.cfg.ExternalURL = strings.TrimRight(cfg.ExternalURL, "/")
	for _, spec := range cfg.Actions.Specs() {
		for _, scope := range spec.Scopes {
			if !slices.Contains(h.scopes, scope) {
				h.scopes = append(h.scopes, scope)
			}
		}
	}
	slices.Sort(h.scopes)
	authed := func(f http.HandlerFunc) http.Handler { return cfg.Identity.Middleware(core.SurfaceAPI, f) }
	mux := http.NewServeMux()
	mux.Handle("POST "+ActionPathPrefix+"{name}", authed(h.invoke))
	mux.HandleFunc("POST "+DeviceStartPath, h.deviceStart)
	mux.HandleFunc("POST "+DeviceTokenPath, h.deviceToken)
	mux.HandleFunc("GET "+DeviceVerifyPath, h.deviceVerify)
	mux.Handle("POST "+DeviceApprovePath, authed(h.deviceApprove))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		h.fail(w, core.NewError(core.CodeNotFound, "no such API route",
			core.NextAction{Action: "factory.status", Description: "see what the factory offers"}))
	})
	return mux, nil
}

// invoke runs one action for the authenticated actor.
func (h *handler) invoke(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		h.fail(w, core.NewError(core.CodeUnauthenticated, "the caller could not be identified"))
		return
	}
	call, err := decodeCall(w, r, h.cfg.MaxBodyBytes)
	if err != nil {
		h.fail(w, err)
		return
	}
	res, err := h.cfg.Actions.Invoke(r.Context(), r.PathValue("name"), core.ActionRequest{Actor: actor,
		Input: call.Input, DryRun: call.DryRun, IdempotencyKey: call.IdempotencyKey, ApprovalID: call.ApprovalID})
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

// fail answers err and logs the cause of an internal failure.
func (h *handler) fail(w http.ResponseWriter, err error) {
	h.logFailure(err)
	writeError(w, err)
}

// logFailure records an error that is the factory's own, with its cause, for
// the operator. Callers never see the cause.
func (h *handler) logFailure(err error) {
	var typed *core.Error
	if !errors.As(err, &typed) || internalCode(typed.Code) {
		cause := err
		if typed != nil && typed.Unwrap() != nil {
			cause = typed.Unwrap()
		}
		h.cfg.Logger.Error("api request failed", "code", string(core.CodeOf(err)), "message", err.Error(), "cause", cause.Error())
	}
}
