package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// Deps are what a Client calls out to.
type Deps struct {
	// HTTP sends the requests; nil means a client with no timeout of its
	// own, since the configured timeout is enforced on Clock.
	HTTP *http.Client
	// Secrets resolves the credential; required when one is configured.
	Secrets secrets.SecretProvider
	// Clock times the requests; nil means the wall clock.
	Clock core.Clock
}

// Client asks one System One backend for typed judgments. It is safe for
// concurrent use.
type Client struct {
	cfg  Config
	deps Deps
}

var _ pkgdecision.SystemOne = (*Client)(nil)

// New checks the configuration and returns a client.
func New(cfg Config, deps Deps) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, ok, _ := cfg.CredentialRef(); ok && deps.Secrets == nil {
		return nil, invalid("system one has a credential configured but no secret provider to resolve it")
	}
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{}
	}
	if deps.Clock == nil {
		deps.Clock = core.SystemClock{}
	}
	return &Client{cfg: cfg, deps: deps}, nil
}

// Judge posts req and returns the judgment only when it is a valid answer
// to req under the pinned version from the configured model. Every other
// outcome is an error and the caller fails closed: UNAVAILABLE when the
// backend cannot be reached or answers with an error, TIMEOUT when it is too
// slow, INVALID_ARGUMENT when its answer breaks the schema.
func (c *Client) Judge(ctx context.Context, req pkgdecision.Request) (pkgdecision.Judgment, error) {
	if len(req.Questions) == 0 || req.SchemaVersion < 1 {
		return pkgdecision.Judgment{}, invalid("a system one request needs typed questions and their schema version")
	}
	if err := req.Policy.Validate(); err != nil {
		return pkgdecision.Judgment{}, core.NewError(core.CodePolicyHold, "a system one request needs pinned policy").WithCause(err)
	}
	body, err := json.Marshal(WireRequest{Version: c.cfg.Version, Backend: c.cfg.Backend, Request: req})
	if err != nil {
		return pkgdecision.Judgment{}, core.NewError(core.CodeInternal, "the system one request cannot be encoded").WithCause(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timedOut, stop := make(chan struct{}), make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-c.deps.Clock.After(c.cfg.Timeout):
			close(timedOut)
			cancel()
		case <-stop:
		}
	}()
	reply, err := c.post(ctx, body)
	select {
	case <-timedOut:
		return pkgdecision.Judgment{}, c.failure(core.CodeTimeout, "did not answer within "+c.cfg.Timeout.String())
	default:
	}
	if err != nil {
		return pkgdecision.Judgment{}, err
	}
	return c.check(reply, req)
}

// post sends the request and reads a bounded reply body.
func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, c.failure(core.CodeInternal, "request could not be built").WithCause(err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(HeaderVersion, c.cfg.Version)
	if ref, ok, err := c.cfg.CredentialRef(); err != nil {
		return nil, err
	} else if ok {
		secret, err := c.deps.Secrets.Resolve(ctx, ref)
		if err != nil || secret.IsZero() {
			// The reference is named, the value never is.
			return nil, c.failure(core.CodeUnavailable, "credential "+ref.String()+" could not be resolved")
		}
		httpReq.Header.Set("Authorization", "Bearer "+string(secret.Reveal()))
	}
	resp, err := c.deps.HTTP.Do(httpReq)
	if err != nil {
		return nil, c.failure(core.CodeUnavailable, "could not be reached").WithCause(errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.failure(core.CodeUnavailable, "answered with HTTP status "+http.StatusText(resp.StatusCode))
	}
	if media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || media != "application/json" {
		return nil, c.failure(core.CodeInvalidArgument, "answered with a body that is not JSON")
	}
	if resp.Header.Get(HeaderVersion) != c.cfg.Version {
		return nil, c.failure(core.CodeInvalidArgument, "answered under another protocol version than "+c.cfg.Version)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxReplyBytes+1))
	switch {
	case err != nil:
		return nil, c.failure(core.CodeUnavailable, "reply could not be read").WithCause(err)
	case len(raw) > MaxReplyBytes:
		return nil, c.failure(core.CodeInvalidArgument, "reply is larger than the adapter accepts")
	}
	return raw, nil
}

// check decodes the reply strictly and validates the judgment against req.
// A judgment is discarded, never repaired.
func (c *Client) check(raw []byte, req pkgdecision.Request) (pkgdecision.Judgment, error) {
	var reply WireReply
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reply); err != nil {
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "reply does not follow the judgment schema")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "reply has data after the judgment")
	}
	j := reply.Judgment
	switch {
	case reply.Version != c.cfg.Version:
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "reply names another protocol version than "+c.cfg.Version)
	case j.Validate(req) != nil:
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "judgment breaks the schema of the typed questions")
	case !j.Model.SameModel(c.cfg.Model):
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "judgment reports a model other than the configured route")
	case len(j.Uncertainties) > maxUncertainties:
		return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "judgment names too many points of doubt")
	}
	for _, u := range j.Uncertainties {
		if !uncertaintyPattern.MatchString(u) {
			return pkgdecision.Judgment{}, c.failure(core.CodeInvalidArgument, "judgment names a point of doubt that is not a short name")
		}
	}
	return j, nil
}

// failure names the backend in a typed error. It never quotes the reply.
func (c *Client) failure(code core.ErrorCode, what string) *core.Error {
	return core.NewError(code, "system one backend "+string(c.cfg.Backend)+" "+what)
}
