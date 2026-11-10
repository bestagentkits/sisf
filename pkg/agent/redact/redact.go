// Package redact removes credentials from text before it is logged, stored
// in an event or returned in an error. It knows the shapes of common provider
// tokens and the exact values of secrets the process was given.
package redact

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Marker replaces every redacted value.
const Marker = "[REDACTED]"

// minSecretLen is the shortest environment value treated as a secret by
// exact match. Shorter values would also match ordinary words.
const minSecretLen = 8

// shape is one token format. hints are lower-case literals of which at least
// one must occur in the text for the pattern to be tried, so ordinary text
// skips the regular expressions altogether.
type shape struct {
	hints []string
	re    *regexp.Regexp
}

// shapes are token formats recognised wherever they appear. The assignment
// pattern keeps the name and drops the value, so a log line stays readable.
var shapes = []shape{
	{[]string{"private key"}, regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)},
	{[]string{"sk-"}, regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{8,}`)},
	{[]string{"sk-"}, regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)},
	{[]string{"gh"}, regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{[]string{"github_pat_"}, regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{[]string{"xox"}, regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{10,}`)},
	{[]string{"akia"}, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{[]string{"aiza"}, regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`)},
	{[]string{"eyj"}, regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
}

var (
	bearer     = shape{[]string{"bearer"}, regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/\-]{12,}=*`)}
	assignment = shape{[]string{"token", "secret", "passw", "api_key", "api-key", "apikey", "credential", "authorization"},
		regexp.MustCompile(`(?i)([A-Za-z0-9_]*(?:token|secret|passw(?:or)?d|api[_-]?key|credential|authorization)[A-Za-z0-9_]*\\?"?\s*[=:]\s*\\?"?)([^\s"',;\\}\]]{6,})`)}
	secretName = regexp.MustCompile(`(?i)token|secret|passw(?:or)?d|api[_-]?key|credential|private[_-]?key|(?:^|_)auth(?:$|_)`)
)

// applies reports whether the pattern is worth trying on text; lower is the
// lower-cased text.
func (sh shape) applies(lower string) bool {
	for _, h := range sh.hints {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// Redactor replaces secrets in text. The zero value is not usable; build one
// with New. It is safe for concurrent use once built.
type Redactor struct {
	replacer *strings.Replacer
}

// New builds a redactor that also knows the exact values of the secrets in
// env (KEY=VALUE entries whose name looks like a credential) and of extra.
func New(env []string, extra ...string) *Redactor {
	seen := map[string]bool{}
	var values []string
	add := func(v string) {
		if len(v) < minSecretLen || seen[v] {
			return
		}
		seen[v] = true
		values = append(values, v)
		// The JSON-escaped form is what appears inside a serialised event.
		if raw, err := json.Marshal(v); err == nil {
			if escaped := string(raw[1 : len(raw)-1]); escaped != v && !seen[escaped] {
				seen[escaped] = true
				values = append(values, escaped)
			}
		}
	}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && secretName.MatchString(name) {
			add(value)
		}
	}
	for _, v := range extra {
		add(v)
	}
	// Longest first, so a secret that contains another is replaced whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, Marker)
	}
	return &Redactor{replacer: strings.NewReplacer(pairs...)}
}

// Text returns s with every known secret and token-shaped value replaced.
func (r *Redactor) Text(s string) string {
	if r != nil && r.replacer != nil {
		s = r.replacer.Replace(s)
	}
	return Text(s)
}

// Bytes is Text for a byte slice.
func (r *Redactor) Bytes(b []byte) []byte { return []byte(r.Text(string(b))) }

// Text redacts token-shaped values only, for callers that hold no secrets of
// their own.
func Text(s string) string {
	lower := strings.ToLower(s)
	for _, sh := range shapes {
		if sh.applies(lower) {
			s = sh.re.ReplaceAllString(s, Marker)
		}
	}
	if bearer.applies(lower) {
		s = bearer.re.ReplaceAllString(s, "${1}"+Marker)
	}
	if assignment.applies(lower) {
		s = assignment.re.ReplaceAllString(s, "${1}"+Marker)
	}
	return s
}
