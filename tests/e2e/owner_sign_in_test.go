//go:build e2e

package e2e

import (
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// Where a simulated install's people sign in: the control plane at its
// external URL and the fake identity provider the simulation runs, both on
// loopback in either mode.
var (
	controlURL           = "http://" + loopbackAddr(install.PortControl)
	fakeIdentityProvider = "http://" + loopbackAddr(install.PortFakeOIDC)
)

// ownerSubject is the Owner of a simulated install at the fake identity
// provider: `sisf init --profile sim` names wiring.SimOIDCIssuer#owner.
const ownerSubject = wiring.SimOwnerSubject

// ownerSignIn holds the Owner's browser on one deployment, signed in once
// through the fake identity provider, the way a person signs in.
type ownerSignIn struct {
	browser *harness.Browser
}

// owner returns the Owner's signed-in browser, signing in on first use.
func (s *ownerSignIn) owner(t *testing.T) *harness.Browser {
	t.Helper()
	if s.browser == nil {
		b := harness.NewBrowser(t, controlURL, fakeIdentityProvider)
		b.SignIn(wiring.ProviderOIDC, ownerSubject)
		s.browser = b
	}
	return s.browser
}

// control is the control plane's base URL and the Owner's session cookie.
func (s *ownerSignIn) control(t *testing.T) (string, string) {
	t.Helper()
	return controlURL, s.owner(t).Session()
}
