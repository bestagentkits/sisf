//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control/api"
)

// The CLI's login lasts an hour and the run longer, so the observer signs
// the operator in again before the stored login lapses, never after.
func TestTheObserverRenewsTheOperatorsLoginBeforeItLapses(t *testing.T) {
	t.Parallel()
	if runDeadline <= api.DefaultDeviceTokenTTL {
		t.Skip("the run fits in one login; nothing to renew")
	}
	for name, c := range map[string]struct {
		expiresIn time.Duration
		renew     bool
	}{
		"fresh login":             {api.DefaultDeviceTokenTTL, false},
		"login about to lapse":    {loginRenewMargin / 2, true},
		"login that lapsed":       {-time.Minute, true},
		"no stored login at all":  {0, true},
		"login just past margins": {loginRenewMargin + time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if c.expiresIn != 0 {
				writeStoredLogin(t, home, time.Now().Add(c.expiresIn))
			}
			renewed := 0
			o := &factoryObserver{home: home, signIn: func(context.Context) { renewed++ }}
			o.keepSignedIn(context.Background())
			if got := renewed == 1; got != c.renew || renewed > 1 {
				t.Fatalf("signed in %d times, want a renewal: %t", renewed, c.renew)
			}
		})
	}
}

// writeStoredLogin writes a stored login of the shape `sisf login` writes.
func writeStoredLogin(t *testing.T, home string, expires time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"url": "http://127.0.0.1:7463", "token": "t", "expires_at": expires.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
