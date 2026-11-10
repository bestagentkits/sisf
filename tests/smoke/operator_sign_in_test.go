//go:build smoke

package smoke

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// signInLimit bounds the CLI's sign-in.
const signInLimit = 2 * time.Minute

// loginCode finds the code `sisf login` asks the person to confirm.
var loginCode = regexp.MustCompile(`confirm the code (\S+)`)

// signInOperator signs the CLI in to the running factory the way an Owner
// does, without anyone at the keyboard: `sisf login` starts the device flow
// and prints a code; a browser signs the Owner in at the development
// install's fake identity provider, confirms their identity and approves
// that code. The token lands owner-only in the install's credentials file,
// which every later `sisf call` with SISF_HOME reads; it is never put on a
// command line or in the environment, and the scrubber learns it at once.
func signInOperator(ctx context.Context, r *commandRunner, sisf, home string, env []string) {
	r.t.Helper()
	base := loopbackURL(install.PortControl, "")
	args := []string{"login", "--url", base, "--dir", home, "--name", "sisf-smoke"}
	r.report.Commands = addUnique(r.report.Commands, sisf+" "+strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(ctx, signInLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, sisf, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = r.dir, env, 5*time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		r.fatalf("sisf login: %v", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		r.fatalf("sisf login: %v", err)
	}
	approved := false
	streamLines(out, func(line string) {
		r.logf("sisf login: %s", r.scrub.clean(line))
		if m := loginCode.FindStringSubmatch(line); m != nil && !approved {
			approveDeviceLogin(r, base, m[1])
			approved = true
		}
	})
	if err := cmd.Wait(); err != nil || !approved {
		r.fatalf("sisf login was not approved: %v", err)
	}
	token, err := storedToken(home)
	if err != nil {
		r.fatalf("the CLI's stored login cannot be read: %v", err)
	}
	r.scrub.addSecret(token)
}

// approveDeviceLogin signs the Owner in at the fake identity provider of
// the development install, confirms their identity, which minting the CLI's
// token needs, and approves the login code.
func approveDeviceLogin(r *commandRunner, base, code string) {
	r.t.Helper()
	b := harness.NewBrowser(r.t, base, loopbackURL(install.PortFakeOIDC, ""))
	b.SignIn(wiring.ProviderOIDC, wiring.SimOwnerSubject)
	b.StepUp()
	body, _ := json.Marshal(map[string]string{"user_code": code})
	resp := b.Do(http.MethodPost, deviceApprovePath, bytes.NewReader(body),
		http.Header{"Content-Type": {"application/json"}, auth.CSRFHeader: {b.CSRF()}})
	if resp.StatusCode != http.StatusOK {
		r.fatalf("the login code was not approved: %d", resp.StatusCode)
	}
	r.logf("approved the CLI's sign-in as %s at the fake identity provider", wiring.SimOwnerSubject)
}

// deviceApprovePath is where a signed-in person approves a CLI login code.
const deviceApprovePath = "/api/v1/device/approve"

// streamLines hands each line of rd to fn until rd ends.
func streamLines(rd io.Reader, fn func(string)) {
	s := bufio.NewScanner(rd)
	for s.Scan() {
		if line := strings.TrimSpace(s.Text()); line != "" {
			fn(line)
		}
	}
}

// storedToken reads the token `sisf login` stored under home.
func storedToken(home string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(home, "credentials.json"))
	if err != nil {
		return "", err
	}
	var c struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	if strings.TrimSpace(c.Token) == "" {
		return "", os.ErrNotExist
	}
	return c.Token, nil
}
