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

// A forced renewal signs in once on the next call, however fresh the
// stored login, and the call after it uses the login as it is again. The
// run that spends nothing forces one so a broken renewal shows before a
// paid run depends on it.
func TestAForcedRenewalSignsInOnceOnTheNextCall(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeStoredLogin(t, home, time.Now().Add(api.DefaultDeviceTokenTTL))
	renewed := 0
	o := &factoryObserver{home: home, signIn: func(context.Context) { renewed++ }}
	o.forceRenewal()
	o.keepSignedIn(context.Background())
	o.keepSignedIn(context.Background())
	if renewed != 1 {
		t.Fatalf("signed in %d times, want exactly once for one forced renewal", renewed)
	}
}

// The end-of-run reads of the ledger and the run go through the observer,
// so a login that lapsed during a long or failed run is renewed first and
// the report still shows what was spent.
func TestTheEndOfRunReadsUseARenewedLogin(t *testing.T) {
	t.Parallel()
	home, bin := t.TempDir(), t.TempDir()
	writeStoredLogin(t, home, time.Now().Add(-time.Minute))
	// A stand-in for the sisf CLI that, like the real one, answers only
	// with a login that has not lapsed: here, the one the renewal wrote.
	sisf := filepath.Join(bin, "sisf")
	script := "#!/bin/sh\ngrep -q renewed \"$SISF_HOME/credentials.json\" || { echo 'sisf: UNAUTHENTICATED: the stored login expired' >&2; exit 1; }\n" +
		`printf '{"output":{"mode":"capped","spent_verified_micros":5}}'` + "\n"
	if err := os.WriteFile(sisf, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &commandRunner{t: t, scrub: newSecretScrubber(nil), report: &smokeReport{}}
	o := &factoryObserver{r: r, sisf: sisf, home: home, env: []string{"SISF_HOME=" + home, "PATH=/usr/bin:/bin"}}
	o.signIn = func(context.Context) {
		raw, _ := json.Marshal(map[string]any{"url": "http://127.0.0.1:7463", "token": "renewed", "expires_at": time.Now().Add(time.Hour)})
		if err := os.WriteFile(filepath.Join(home, "credentials.json"), raw, 0o600); err != nil {
			t.Error(err)
		}
	}
	b, err := o.budget(context.Background())
	if err != nil || b.spent() != 5 {
		t.Fatalf("budget.get after the login lapsed: spent %d, error %v", b.spent(), err)
	}
}
