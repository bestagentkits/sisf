//go:build smoke

package smoke

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// devUnlockPath is where the development install's fake identity provider
// takes the sign-in secret.
const devUnlockPath = "/_sim/unlock"

// operatorBrowser opens the Owner's browser on the factory and unlocks
// sign-in at the development install's fake identity provider with the
// secret `sisf up` wrote to the install's control secrets: the operator
// reads that file, no agent is given it. The browser keeps only the
// provider's HMAC of the secret in its cookie jar. The scrubber already knows the
// secret (seedInstallSecrets reads every secret file after `up`).
func operatorBrowser(r *commandRunner, base, home string) *harness.Browser {
	r.t.Helper()
	raw, err := os.ReadFile(install.Layout{Dir: home}.DevSignInSecret())
	if err != nil {
		r.fatalf("the development sign-in secret cannot be read: %v", err)
	}
	secret := strings.TrimSpace(string(raw))
	r.scrub.addSecret(secret)
	b := harness.NewBrowser(r.t, base, loopbackURL(install.PortFakeOIDC, ""))
	form := url.Values{"secret": {secret}}.Encode()
	// The unlock form posts from the provider's own page, so it carries the
	// provider's origin; a locked provider takes a POST from nowhere else.
	resp := b.Do(http.MethodPost, loopbackURL(install.PortFakeOIDC, devUnlockPath), strings.NewReader(form),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {b.ProviderOrigin()}})
	if resp.StatusCode != http.StatusOK {
		r.fatalf("the development sign-in was not unlocked: %d", resp.StatusCode)
	}
	return b
}
