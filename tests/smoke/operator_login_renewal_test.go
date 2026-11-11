//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// loginRenewMargin is how long before the stored login lapses the observer
// signs in again. The CLI's login lasts an hour (the control plane's device
// token lifetime) and the run takes longer; a call started inside the
// margin still finishes on the old login, as a call is bounded to a minute.
const loginRenewMargin = 5 * time.Minute

// storedLogin is the part of the login `sisf login` stored that the run reads.
type storedLogin struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// readStoredLogin reads the login `sisf login` stored under home.
func readStoredLogin(home string) (storedLogin, error) {
	raw, err := os.ReadFile(filepath.Join(home, "credentials.json"))
	if err != nil {
		return storedLogin{}, err
	}
	var l storedLogin
	if err := json.Unmarshal(raw, &l); err != nil {
		return storedLogin{}, err
	}
	if strings.TrimSpace(l.Token) == "" {
		return storedLogin{}, os.ErrNotExist
	}
	return l, nil
}

// keepSignedIn signs the operator in again when the stored login is
// missing or lapses within loginRenewMargin, the way an Owner runs
// `sisf login` again; the token lifetime itself stays as every install has it.
func (o *factoryObserver) keepSignedIn(ctx context.Context) {
	if o.signIn == nil {
		return
	}
	if l, err := readStoredLogin(o.home); err == nil && time.Until(l.ExpiresAt) > loginRenewMargin {
		return
	}
	o.signIn(ctx)
}
