package oauthas

import (
	"context"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// accessTokenType is the JOSE type of an access token (RFC 9068). Checking
// it stops any other JWT signed with the same key passing as one.
const accessTokenType = "at+jwt"

const maxTokenBytes = 8 << 10

// accessClaims are the claims of an access token.
type accessClaims struct {
	jwt.Claims
	Scope    string `json:"scope"`
	ClientID string `json:"client_id"`
	// SessionID is the handle of the browser session the grant came from.
	SessionID string `json:"sid"`
}

// mintAccessToken signs a token for a redeemed grant.
func (s *Server) mintAccessToken(g codeGrant) (string, time.Time, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.cfg.SigningKey},
		(&jose.SignerOptions{}).WithType(accessTokenType))
	if err != nil {
		return "", time.Time{}, core.NewError(core.CodeInternal, "the token could not be signed").WithCause(err)
	}
	now := s.cfg.Clock.Now()
	exp := now.Add(s.cfg.AccessTokenTTL)
	claims := accessClaims{
		Claims: jwt.Claims{Issuer: s.cfg.Issuer, Subject: g.actorID, Audience: jwt.Audience{g.resource},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(exp), ID: g.tokenID},
		Scope: strings.Join(g.scopes, " "), ClientID: g.clientID, SessionID: g.session,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", time.Time{}, core.NewError(core.CodeInternal, "the token could not be signed").WithCause(err)
	}
	return raw, exp, nil
}

// VerifyAccessToken checks a bearer token presented to the protected
// resource and returns the actor it stands for: the person, with the granted
// scopes and the session handle, so the authorizer re-checks the session and
// the person's roles at action time. The signature, type, issuer, audience,
// lifetime, revocation and the session behind the token must all hold; any
// failure is UNAUTHENTICATED. The actor never carries a step-up.
func (s *Server) VerifyAccessToken(ctx context.Context, raw string) (core.Actor, error) {
	if len(raw) == 0 || len(raw) > maxTokenBytes {
		return core.Actor{}, unauthenticated("the access token is malformed")
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(tok.Headers) != 1 {
		return core.Actor{}, unauthenticated("the access token is malformed")
	}
	if typ, _ := tok.Headers[0].ExtraHeaders[jose.HeaderType].(string); !strings.EqualFold(typ, accessTokenType) {
		return core.Actor{}, unauthenticated("the token is not an access token")
	}
	var c accessClaims
	if err := tok.Claims(&s.cfg.SigningKey.PublicKey, &c); err != nil {
		return core.Actor{}, unauthenticated("the access token signature does not verify")
	}
	if err := s.checkClaims(c); err != nil {
		return core.Actor{}, err
	}
	scopes, ok := parseScopeParam(c.Scope)
	switch {
	case !ok || len(scopes) == 0:
		return core.Actor{}, unauthenticated("the access token carries no valid scope")
	case s.isRevoked(c.ID):
		return core.Actor{}, unauthenticated("the access token was revoked")
	case !s.cfg.Sessions.Active(ctx, c.SessionID):
		return core.Actor{}, unauthenticated("the session behind the access token has ended; authorize the client again")
	}
	return core.Actor{ID: c.Subject, Kind: core.ActorHuman, Scopes: scopes, SessionID: c.SessionID}, nil
}

// checkClaims validates issuer, audience and lifetime against this server.
func (s *Server) checkClaims(c accessClaims) error {
	now := s.cfg.Clock.Now()
	switch {
	case c.Issuer != s.cfg.Issuer:
		return unauthenticated("the access token was issued by another issuer")
	case len(c.Audience) != 1 || c.Audience[0] != s.cfg.Resource:
		return unauthenticated("the access token is for another resource")
	case c.Expiry == nil || !now.Before(c.Expiry.Time()):
		return unauthenticated("the access token has expired")
	case c.IssuedAt == nil || c.IssuedAt.Time().After(now):
		return unauthenticated("the access token is not valid yet")
	case c.NotBefore != nil && c.NotBefore.Time().After(now):
		return unauthenticated("the access token is not valid yet")
	case c.Subject == "" || c.SessionID == "" || c.ID == "":
		return unauthenticated("the access token is incomplete")
	}
	return nil
}

func unauthenticated(message string) *core.Error {
	return core.NewError(core.CodeUnauthenticated, message)
}
