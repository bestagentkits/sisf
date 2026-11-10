// Package identity defines two separate contracts: who a person is when they
// sign in, and what the factory may do to a repository.
//
// A login identity comes from an identity provider and is keyed by issuer and
// subject. Repository permission comes from an app installation and is used
// through minimal, short-lived installation tokens. Neither implies the other:
// signing in with GitHub grants nothing on a repository, and an installation
// signs nobody in.
package identity

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// Identity is a verified login identity. Issuer and Subject together are the
// identity; Email is a contact attribute and never establishes who someone is.
type Identity struct {
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	// Login and Name are display attributes.
	Login string `json:"login,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Key returns "issuer#subject", the stable key an internal actor is mapped
// from. An issuer never contains "#", so the key cannot be forged by a
// crafted subject.
func (i Identity) Key() string { return i.Issuer + "#" + i.Subject }

// Same reports whether both identities are the same issuer and subject.
// Email is ignored, and an incomplete identity is the same as nobody.
func (i Identity) Same(other Identity) bool {
	return i.Issuer != "" && i.Subject != "" && i.Issuer == other.Issuer && i.Subject == other.Subject
}

// Validate requires an absolute issuer URL without a fragment and a subject.
func (i Identity) Validate() error {
	u, err := url.Parse(i.Issuer)
	switch {
	case err != nil || u.Scheme == "" || u.Host == "" || u.Fragment != "" || strings.ContainsAny(i.Issuer, "# \t\r\n"):
		return core.NewError(core.CodeInvalidArgument, "identity needs an absolute issuer URL without a fragment")
	case i.Subject == "":
		return core.NewError(core.CodeInvalidArgument, "identity needs a subject; an email alone is not an identity")
	}
	return nil
}

// AuthRequest starts a sign-in. State and Nonce are generated per attempt and
// bound to the browser session that started it.
type AuthRequest struct {
	State         string
	Nonce         string
	RedirectURI   string
	CodeChallenge string
}

// Exchange completes a sign-in with the values the provider sent back and the
// ones the session kept.
type Exchange struct {
	Code         string
	Nonce        string
	RedirectURI  string
	CodeVerifier string
}

// Provider signs people in. It returns an identity only after validating the
// provider's response in full: signature, issuer, audience, expiry and nonce.
// Any failed check is UNAUTHENTICATED; there is no partial identity.
type Provider interface {
	// Name is the configured provider name, for example "github" or "google".
	Name() string
	// Issuer is the issuer every identity from this provider carries.
	Issuer() string
	AuthorizeURL(req AuthRequest) (string, error)
	Exchange(ctx context.Context, ex Exchange) (Identity, error)
}

// Installation is the factory's app installation on one repository. It is
// the source of repository permission and is unrelated to any login.
type Installation struct {
	ID          int64             `json:"id"`
	Owner       string            `json:"owner"`
	Repo        string            `json:"repo"`
	Permissions map[string]string `json:"permissions,omitempty"`
	Suspended   bool              `json:"suspended,omitempty"`
}

// Covers reports whether the installation is active on owner/repo.
func (i Installation) Covers(owner, repo string) bool {
	return !i.Suspended && i.ID != 0 && i.Owner == owner && i.Repo == repo
}

// TokenRequest asks for an installation token narrowed to the repositories
// and permissions one task needs.
type TokenRequest struct {
	InstallationID int64
	Repositories   []string
	Permissions    map[string]string
}

// InstallationToken is a short-lived repository credential. It stays in the
// control plane: an agent running untrusted code never receives it.
type InstallationToken struct {
	Token       core.SecretValue  `json:"-"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

// Expired reports whether the token may no longer be used.
func (t InstallationToken) Expired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

// InstallationProvider resolves repository permission and issues, rotates and
// revokes installation tokens.
type InstallationProvider interface {
	// Installation returns NOT_FOUND when the app is not installed on the repository.
	Installation(ctx context.Context, owner, repo string) (Installation, error)
	Token(ctx context.Context, req TokenRequest) (InstallationToken, error)
	Revoke(ctx context.Context, token InstallationToken) error
}
