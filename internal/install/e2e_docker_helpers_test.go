//go:build e2e

package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The live checks run real containers. Everything they create is named
// "sisf-e2e-<random>" and removed when the test ends; nothing else on the
// machine is read, stopped or changed.

// e2eImageEnv overrides the image of the throwaway PostgreSQL server, so a
// machine that already holds a suitable image need not fetch the pinned one.
const e2eImageEnv = "SISF_E2E_POSTGRES_IMAGE"

// e2eRunnerImageEnv overrides the small image the runner-like container uses.
const e2eRunnerImageEnv = "SISF_E2E_RUNNER_IMAGE"

// e2eName returns a fresh "sisf-e2e-" name.
func e2eName(t *testing.T, what string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "sisf-e2e-" + what + "-" + hex.EncodeToString(b)
}

// randomToken returns a fresh random secret for a throwaway server.
func randomToken(t *testing.T) string {
	t.Helper()
	token, err := newToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// dockerError is a failed docker command with the tail of its error output.
type dockerError struct {
	args   []string
	stderr string
	cause  error
}

func (e *dockerError) Error() string {
	return "docker " + e.args[0] + ": " + e.cause.Error() + ": " + e.stderr
}

// docker runs the docker CLI and returns its trimmed standard output.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// Not t.Context(): cleanup runs after it is cancelled, and a container must
	// still be removed then.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), &dockerError{args: args, stderr: strings.TrimSpace(stderr.String()), cause: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// requireDocker skips the test when no Docker daemon answers.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := docker(t, "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
}

// imageOr returns the image named by the environment variable, or the default.
func imageOr(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}

// removeLater removes a container and its anonymous volumes when the test
// ends, whether it passed or not.
func removeLater(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := docker(t, "rm", "-f", "-v", name); err != nil && !strings.Contains(err.Error(), "No such container") {
			t.Errorf("cleanup of container %s failed: %v", name, err)
		}
	})
}

// removeNetworkLater removes a network when the test ends.
func removeNetworkLater(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := docker(t, "network", "rm", name); err != nil && !strings.Contains(err.Error(), "not found") {
			t.Errorf("cleanup of network %s failed: %v", name, err)
		}
	})
}
