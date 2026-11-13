package managedapp

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	namePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	volumePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)
	envNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)
	userPattern     = regexp.MustCompile(`^([0-9]+):([0-9]+)$`)
	pinnedPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	secretLikeNames = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE|API_KEY|_KEY)$`)
	methods         = map[string]bool{"": true, "GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
)

// reservedEnv are variables the factory sets; a Spec cannot.
var reservedEnv = map[string]bool{"SISF_REVISION": true}

// Validate reports every problem of s, sorted, or nil when it has none.
func (s *Spec) Validate() []string {
	var p problems
	if s.APIVersion != APIVersion {
		p.addf("apiVersion is %q, want %q", s.APIVersion, APIVersion)
	}
	if !namePattern.MatchString(s.Name) {
		p.addf("name %q is not 2-31 lowercase letters, digits or dashes starting with a letter", s.Name)
	}
	s.Runtime.validate(&p)
	s.Bundle.validate(&p)
	volumes := map[string]bool{}
	for _, v := range s.Data {
		v.validate(&p, "data", volumes)
	}
	deps := map[string]bool{}
	for _, d := range s.Dependencies {
		d.validate(&p, deps, volumes)
	}
	s.validateChecks(&p)
	s.Policy.validate(&p)
	return p.sorted()
}

func (r Runtime) validate(p *problems) {
	if !pinnedPattern.MatchString(r.Image) {
		p.addf("runtime.image %q is not pinned by a sha256 digest", r.Image)
	}
	if r.Port < 1 || r.Port > 65535 {
		p.addf("runtime.port %d is not a port", r.Port)
	}
	validateUser(p, "runtime.user", r.User, true)
	if !strings.HasPrefix(r.HealthPath, "/") {
		p.addf("runtime.health_path %q does not start with /", r.HealthPath)
	}
	validateEnv(p, "runtime", r.Env, r.Secrets)
}

func (b Bundle) validate(p *problems) {
	validateRelative(p, "bundle.lock", b.Lock)
	if !path.IsAbs(b.Mount) || path.Clean(b.Mount) != b.Mount || b.Mount == "/" {
		p.addf("bundle.mount %q is not a clean absolute path below /", b.Mount)
	}
	for i, src := range b.Sources {
		validateRelative(p, fmt.Sprintf("bundle.sources[%d].from", i), src.From)
		validateRelative(p, fmt.Sprintf("bundle.sources[%d].to", i), src.To)
	}
	for i, pr := range b.Prune {
		validateRelative(p, fmt.Sprintf("bundle.prune[%d]", i), pr)
	}
}

func (v Volume) validate(p *problems, where string, seen map[string]bool) {
	if !volumePattern.MatchString(v.Name) {
		p.addf("%s volume name %q is not lowercase letters, digits, - or _", where, v.Name)
	}
	if seen[v.Name] {
		p.addf("%s volume %q is declared twice", where, v.Name)
	}
	seen[v.Name] = true
	if !path.IsAbs(v.Target) || path.Clean(v.Target) != v.Target || v.Target == "/" {
		p.addf("%s volume %q target %q is not a clean absolute path below /", where, v.Name, v.Target)
	}
	if v.Owner != "" {
		validateUser(p, where+" volume "+v.Name+" owner", v.Owner, false)
	}
}

func (d Dependency) validate(p *problems, seen, volumes map[string]bool) {
	where := "dependency " + d.Name
	if !namePattern.MatchString(d.Name) {
		p.addf("dependency name %q is not 2-31 lowercase letters, digits or dashes starting with a letter", d.Name)
	}
	if seen[d.Name] {
		p.addf("%s is declared twice", where)
	}
	seen[d.Name] = true
	if !pinnedPattern.MatchString(d.Image) {
		p.addf("%s image %q is not pinned by a sha256 digest", where, d.Image)
	}
	validateEnv(p, where, d.Env, d.Secrets)
	for _, v := range d.Data {
		v.validate(p, where, volumes)
	}
	if (len(d.Backup) == 0) != (len(d.Restore) == 0) {
		p.addf("%s declares only one of backup and restore", where)
	}
}

func (s *Spec) validateChecks(p *problems) {
	if len(s.Checks) == 0 {
		p.addf("checks: at least one check is needed; without one no revision can be judged")
	}
	seen := map[string]bool{}
	for _, c := range s.Checks {
		if !namePattern.MatchString(c.Name) {
			p.addf("check name %q is not 2-31 lowercase letters, digits or dashes starting with a letter", c.Name)
		}
		if seen[c.Name] {
			p.addf("check %q is declared twice", c.Name)
		}
		seen[c.Name] = true
		if len(c.Steps) == 0 {
			p.addf("check %q has no steps", c.Name)
		}
		for i, st := range c.Steps {
			if !methods[strings.ToUpper(st.Method)] {
				p.addf("check %q step %d method %q is not an HTTP method", c.Name, i, st.Method)
			}
			if !strings.HasPrefix(st.Path, "/") {
				p.addf("check %q step %d path %q does not start with /", c.Name, i, st.Path)
			}
		}
	}
}

func (pol Policy) validate(p *problems) {
	for _, pat := range pol.Protected {
		if _, err := path.Match(pat, ""); err != nil || pat == "" || strings.HasPrefix(pat, "/") {
			p.addf("policy.protected pattern %q is not a relative path pattern", pat)
		}
	}
	for c, d := range pol.LockChanges {
		switch c {
		case LockPatch, LockMinor, LockMajor, LockAdded, LockRemoved, LockCore:
		default:
			p.addf("policy.lock_changes names an unknown class %q", c)
		}
		if d != DecisionAuto && d != DecisionOwner {
			p.addf("policy.lock_changes.%s is %q, want auto or owner", c, d)
		}
	}
}

// ProtectedPaths returns the repository path patterns a candidate may not
// change on its own: the Spec, its lockfile, and the Owner's patterns.
func (s *Spec) ProtectedPaths() []string {
	out := append([]string{FileName, s.Bundle.Lock}, s.Policy.Protected...)
	sort.Strings(out)
	return compact(out)
}

func validateUser(p *problems, where, user string, nonRoot bool) {
	m := userPattern.FindStringSubmatch(user)
	if m == nil {
		p.addf("%s %q is not uid:gid", where, user)
		return
	}
	if nonRoot && m[1] == "0" {
		p.addf("%s runs as root; a managed application never does", where)
	}
}

func validateEnv(p *problems, where string, env, secrets map[string]string) {
	for k := range env {
		switch {
		case !envNamePattern.MatchString(k):
			p.addf("%s env %q is not a variable name", where, k)
		case reservedEnv[k]:
			p.addf("%s env %s is set by the factory", where, k)
		case secretLikeNames.MatchString(k):
			p.addf("%s env %s looks like a secret; declare it under secrets", where, k)
		}
	}
	for k, ref := range secrets {
		if !envNamePattern.MatchString(k) || reservedEnv[k] {
			p.addf("%s secret variable %q is not a variable name the application may set", where, k)
		}
		if !secretPattern.MatchString(ref) {
			p.addf("%s secret %s names %q, not a secret name", where, k, ref)
		}
		if _, dup := env[k]; dup {
			p.addf("%s sets %s both as env and as a secret", where, k)
		}
	}
}

func validateRelative(p *problems, where, rel string) {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		p.addf("%s %q is not a clean relative path inside the repository", where, rel)
	}
}

type problems []string

func (p *problems) addf(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) sorted() []string {
	if len(p) == 0 {
		return nil
	}
	out := append([]string(nil), p...)
	sort.Strings(out)
	return out
}

func compact(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
