//go:build smoke

package smoke

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/install"
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
			approveDeviceLogin(r, base, home, m[1])
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
// the development install, confirms their identity for approving exactly
// this login code, and approves it from the verification page's form.
func approveDeviceLogin(r *commandRunner, base, home, code string) {
	r.t.Helper()
	b := operatorBrowser(r, base, home)
	b.SignIn(wiring.ProviderOIDC, wiring.SimOwnerSubject)
	b.ApproveDevice(code)
	r.logf("approved the CLI's sign-in as %s at the fake identity provider", wiring.SimOwnerSubject)
}

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
