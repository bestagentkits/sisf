// Package agentenv builds the whole environment of a coding or QA agent
// process from an explicit allow-list. An agent never inherits the
// environment of the process that starts it: it gets a PATH, a private HOME
// and TMPDIR, the provider credential variables its tool reads, and the
// variables the operator named, and nothing else. Names of the factory's
// own variables are refused outright, whatever the operator asks.
package agentenv

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DefaultPath is the agent's PATH when the starting process has none.
const DefaultPath = "/usr/local/bin:/usr/bin:/bin"

// credentials are the variables each agent tool reads its provider
// credentials from.
var credentials = map[string][]string{
	"claude": {"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
	"codex":  {"OPENAI_API_KEY", "CODEX_API_KEY"},
	"pi":     nil,
}

// refusedPrefixes start the names of the factory's own variables: its
// configuration and secrets, the edge's and the Docker client's.
var refusedPrefixes = []string{"SISF_", "EDGE_", "DOCKER_"}

// managed are set by Build itself and cannot be passed through.
var managed = []string{"PATH", "HOME", "TMPDIR"}

var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// Credentials returns the provider credential variables agent reads, and
// whether agent is a tool this package knows.
func Credentials(agent string) ([]string, bool) {
	vars, ok := credentials[agent]
	return slices.Clone(vars), ok
}

// Spec is what an agent's environment is built from.
type Spec struct {
	// Agent is claude, codex or pi.
	Agent string
	// Home is the agent's absolute HOME: a private one, or the operator's
	// own where CheckOperatorHome allowed it.
	Home string
	// TempDir is the agent's absolute TMPDIR, always private; Home/tmp
	// when empty.
	TempDir string
	// Extra are further variable names the operator allowed.
	Extra []string
	// Forbidden are further exact names that must never be passed, such as
	// the starting process's own secret variables.
	Forbidden []string
	// LookupEnv reads the starting process's environment.
	LookupEnv func(string) (string, bool)
}

// Build returns the agent's whole environment, KEY=VALUE. A credential or
// extra variable that is not set is left out. A malformed name is
// INVALID_ARGUMENT; a name that is forbidden, managed here or starts with
// a refused prefix is PERMISSION_DENIED.
func Build(s Spec) ([]string, error) {
	vars, known := Credentials(s.Agent)
	switch {
	case !known:
		return nil, core.NewError(core.CodeInvalidArgument, "unknown agent "+s.Agent)
	case s.LookupEnv == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the agent environment needs a way to read variables")
	case !filepath.IsAbs(s.Home):
		return nil, core.NewError(core.CodeInvalidArgument, "the agent's HOME must be an absolute path")
	case s.TempDir != "" && !filepath.IsAbs(s.TempDir):
		return nil, core.NewError(core.CodeInvalidArgument, "the agent's TMPDIR must be an absolute path")
	}
	tmp := s.TempDir
	if tmp == "" {
		tmp = filepath.Join(s.Home, "tmp")
	}
	path, ok := s.LookupEnv("PATH")
	if !ok || path == "" {
		path = DefaultPath
	}
	env := []string{"PATH=" + path, "HOME=" + s.Home, "TMPDIR=" + tmp}
	for _, n := range append(vars, s.Extra...) {
		if err := Allowed(n, s.Forbidden...); err != nil {
			return nil, err
		}
		if value, set := s.LookupEnv(n); set && !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, n+"=") }) {
			env = append(env, n+"="+value)
		}
	}
	return env, nil
}

// Allowed checks that a variable name may be passed to an agent.
func Allowed(n string, forbidden ...string) error {
	upper := strings.ToUpper(n)
	switch {
	case !name.MatchString(n):
		return core.NewError(core.CodeInvalidArgument, "a variable passed to an agent is not a variable name")
	case slices.Contains(managed, upper) || slices.Contains(forbidden, n) ||
		slices.ContainsFunc(refusedPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) }):
		return core.NewError(core.CodePermissionDenied, "an agent may not see the factory's variable "+n)
	}
	return nil
}

// Names splits a comma-separated list of variable names, dropping blanks.
func Names(csv string) []string {
	var out []string
	for n := range strings.SplitSeq(csv, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
