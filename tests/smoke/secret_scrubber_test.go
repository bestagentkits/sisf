//go:build smoke

package smoke

import (
	"bufio"
	"strings"
	"sync"

	"github.com/bestagentkits/sisf/internal/redact"
)

// secretScrubber is the single gate every string passes before it reaches
// the test log or the report. It is seeded with the caller's environment,
// whose credential-named values are secrets, and grows as the run learns
// further secrets: the gh token, the CLI token the operator's sign-in stored, the values
// of the install's env files and its other secret files.
type secretScrubber struct {
	mu      sync.Mutex
	env     []string
	secrets []string
	r       *redact.Redactor
}

func newSecretScrubber(env []string, secrets ...string) *secretScrubber {
	s := &secretScrubber{env: append([]string(nil), env...)}
	for _, v := range secrets {
		s.addLocked(v)
	}
	s.rebuildLocked()
	return s
}

// addSecret makes value redacted from now on. Empty values are ignored.
func (s *secretScrubber) addSecret(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addLocked(value) {
		s.rebuildLocked()
	}
}

// addEnvFile treats every KEY=VALUE line of an env file as environment:
// values under a credential-like name become secrets.
func (s *secretScrubber) addEnvFile(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		s.env = append(s.env, line)
	}
	s.rebuildLocked()
}

// clean returns text without any known secret, credential shape or
// personal data.
func (s *secretScrubber) clean(text string) string {
	s.mu.Lock()
	r := s.r
	s.mu.Unlock()
	return r.Redact(text)
}

func (s *secretScrubber) addLocked(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	s.secrets = append(s.secrets, value)
	return true
}

func (s *secretScrubber) rebuildLocked() {
	s.r = redact.New(redact.Config{Env: s.env, Secrets: s.secrets})
}
