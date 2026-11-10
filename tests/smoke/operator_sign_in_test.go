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
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

// signInLimit is how long the operator has to approve the CLI's sign-in.
const signInLimit = 10 * time.Minute

// signInOperator signs the CLI in to the running factory the way an Owner
// does: `sisf login` prints a verification URL and a code, the operator
// opens it, signs in with the install's provider, confirms their identity
// and approves. The URL is streamed to the test log as soon as it is
// printed. The token lands owner-only in the install's credentials file,
// which every later `sisf call` with SISF_HOME reads; it is never put on a
// command line or in the environment, and the scrubber learns it at once.
func signInOperator(ctx context.Context, r *commandRunner, sisf, home string, env []string) {
	r.t.Helper()
	args := []string{"login", "--url", loopbackURL(install.PortControl, ""), "--dir", home, "--name", "sisf-smoke"}
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
	r.logf("ACTION NEEDED: approve the CLI sign-in in a browser within %s", signInLimit)
	streamLines(out, func(line string) { r.logf("sisf login: %s", line) })
	if err := cmd.Wait(); err != nil {
		r.fatalf("sisf login was not approved: %v", err)
	}
	token, err := storedToken(home)
	if err != nil {
		r.fatalf("the CLI's stored login cannot be read: %v", err)
	}
	r.scrub.addSecret(token)
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
