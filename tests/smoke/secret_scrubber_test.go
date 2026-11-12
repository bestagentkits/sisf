//go:build smoke

package smoke

import (
	"bufio"
	"regexp"
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
	mu       sync.Mutex
	env      []string
	secrets  []string
	personal []personalValue
	r        *redact.Redactor
}

// personalValue is personal data the run knows of, such as the gh login or
// the operator's home directory, and the placeholder that stands in for it.
type personalValue struct {
	re          *regexp.Regexp
	placeholder string
}

// notNameChar is what must border a personal value for it to be that value
// rather than part of a longer name.
const notNameChar = `[^A-Za-z0-9_.-]`

// addPersonal replaces value, matched case-insensitively as a whole name or
// path, with placeholder from now on. Empty values are ignored.
func (s *secretScrubber) addPersonal(value, placeholder string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	re := regexp.MustCompile(`(?i)(^|` + notNameChar + `)` + regexp.QuoteMeta(value) + `($|` + notNameChar + `)`)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personal = append(s.personal, personalValue{re: re, placeholder: strings.ReplaceAll(placeholder, "$", "$$")})
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
	r, personal := s.r, s.personal
	s.mu.Unlock()
	text = r.Redact(text)
	for _, p := range personal {
		// A match takes the boundary after it, so a second pass catches a
		// value that directly follows another.
		for range 2 {
			text = p.re.ReplaceAllString(text, "${1}"+p.placeholder+"${2}")
		}
	}
	return text
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
