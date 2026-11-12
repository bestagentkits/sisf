package install

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// File modes of an install. Secrets are readable by their owner only.
const (
	secretDirMode  fs.FileMode = 0o700
	secretFileMode fs.FileMode = 0o600
)

// tokenBytes is the entropy of a generated token or password: 256 bits.
const tokenBytes = 32

// newToken returns a random token of 43 URL-safe characters. The alphabet
// has no character that a connection string, a shell or a Compose env file
// would treat specially.
func newToken(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", unavailablef(err, "no random bytes are available to make a token")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// writeSecret writes data to path for its owner only. The file is written
// beside its target and renamed into place, so a reader never sees half a
// secret, and its mode is set before any content is written.
func writeSecret(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, secretDirMode); err != nil {
		return unavailablef(err, "cannot create %s", dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return unavailablef(err, "cannot create a file in %s", dir)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after a successful rename; cleanup after a failure
	if err := tmp.Chmod(secretFileMode); err != nil {
		_ = tmp.Close()
		return unavailablef(err, "cannot restrict %s", path)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return unavailablef(err, "cannot write %s", path)
	}
	if err := tmp.Close(); err != nil {
		return unavailablef(err, "cannot write %s", path)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return unavailablef(err, "cannot place %s", path)
	}
	return nil
}

// readSecret reads a secret file. Its content is returned wrapped, so a
// careless print shows a marker and not the value.
func readSecret(path string) (core.SecretValue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.SecretValue{}, unavailablef(err, "cannot read %s", path)
	}
	return core.NewSecretValue([]byte(strings.TrimSpace(string(data)))), nil
}

var (
	envKeyPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	envValuePattern = regexp.MustCompile(`^[A-Za-z0-9_@:/.,=+?&%-]*$`)
)

// formatEnv renders KEY=VALUE lines from alternating keys and values. Only
// plain values are accepted: Compose would expand "$", strip quotes and cut
// at "#", and a newline would start another variable. The error names the
// key and never the value.
func formatEnv(pairs ...string) ([]byte, error) {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		key, value := pairs[i], pairs[i+1]
		if !envKeyPattern.MatchString(key) {
			return nil, invalidf("environment variable name %q is not valid", key)
		}
		if !envValuePattern.MatchString(value) {
			return nil, invalidf("the value of %s has a character an env file cannot carry safely", key)
		}
		fmt.Fprintf(&b, "%s=%s\n", key, value)
	}
	return []byte(b.String()), nil
}

// parseEnv reads an env file written by formatEnv.
func parseEnv(data []byte) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && envKeyPattern.MatchString(key) {
			out[key] = value
		}
	}
	return out
}

// readEnvFile reads and parses an env file.
func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, unavailablef(err, "cannot read %s", path)
	}
	return parseEnv(data), nil
}
