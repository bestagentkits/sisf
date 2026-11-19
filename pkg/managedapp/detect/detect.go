// Package detect writes a managedapp.Spec from what an Owner already has: a
// Compose project or a source directory. The part of adoption every stack
// shares (runtime, data, dependencies, secrets) is read from the Compose file
// here; what one stack knows (how its content is bundled, which behaviour to
// check, which changes are sensitive) lives only in that stack's Detector.
//
// Adoption never copies a secret value: a secret is named, and the Owner
// keeps its value where the factory's secret store reads it.
package detect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// Detector knows one stack.
type Detector interface {
	// Name names the stack; it is written to the Spec's stack field.
	Name() string
	// Match reports whether app is of this stack.
	Match(app *App) bool
	// Describe fills in what is particular to the stack.
	Describe(app *App, s *managedapp.Spec) error
}

// Options are what the Owner tells adoption that it cannot read itself.
type Options struct {
	// Root is the repository root; Dir must be inside it. Empty means Dir.
	Root string
	// Port the program listens on, for stacks that do not publish one.
	Port int
	// Env is configuration that is not secret.
	Env map[string]string
	// Data are named volumes the application writes to.
	Data []managedapp.Volume
}

// App is what adoption found in the Owner's directory.
type App struct {
	Dir     string
	opts    Options
	compose *composeFile
	// web is the Compose service that serves traffic; empty without Compose.
	web string
	// notes are what the Owner should review in the written Spec.
	notes []string
}

// Note records something the Owner should review.
func (a *App) Note(format string, args ...any) {
	a.notes = append(a.notes, fmt.Sprintf(format, args...))
}

// relDir returns Dir relative to the repository root.
func (a *App) relDir() string {
	root := a.opts.Root
	if root == "" {
		return "."
	}
	rel, err := filepath.Rel(root, a.Dir)
	if err != nil {
		return "."
	}
	return rel
}

// HasFile reports whether the regular file rel exists in the app directory.
func (a *App) HasFile(rel string) bool {
	fi, err := os.Lstat(filepath.Join(a.Dir, rel))
	return err == nil && fi.Mode().IsRegular()
}

// webService returns the service that serves traffic.
func (a *App) webService() composeService { return a.compose.Services[a.web] }

// Detectors are the stacks adoption knows, tried in order.
var Detectors = []Detector{wordpress{}, goProgram{}}

// Result is a written Spec and what the Owner should review in it.
type Result struct {
	Spec  *managedapp.Spec
	Notes []string
}

// Adopt describes the application in dir as a Spec named name.
func Adopt(dir, name string, opts Options) (*Result, error) {
	app := &App{Dir: dir, opts: opts}
	if path := filepath.Join(dir, "compose.yaml"); fileExists(path) {
		f, err := readCompose(path)
		if err != nil {
			return nil, err
		}
		app.compose = f
		if app.web, err = webServiceOf(f); err != nil {
			return nil, err
		}
	}
	var d Detector
	for _, cand := range Detectors {
		if cand.Match(app) {
			d = cand
			break
		}
	}
	if d == nil {
		return nil, errors.New("no known stack matches " + dir + "; known: " + knownStacks())
	}
	s := &managedapp.Spec{APIVersion: managedapp.APIVersion, Name: name, Stack: d.Name()}
	if app.compose != nil {
		if err := describeFromCompose(app, s); err != nil {
			return nil, err
		}
	}
	if err := d.Describe(app, s); err != nil {
		return nil, err
	}
	if problems := s.Validate(); len(problems) > 0 {
		return nil, &managedapp.InvalidError{Problems: problems}
	}
	return &Result{Spec: s, Notes: app.notes}, nil
}

// webServiceOf returns the one service that publishes a port.
func webServiceOf(f *composeFile) (string, error) {
	var webs []string
	for _, n := range f.serviceNames() {
		if f.Services[n].containerPort() > 0 {
			webs = append(webs, n)
		}
	}
	if len(webs) != 1 {
		return "", fmt.Errorf("adoption needs exactly one service that publishes a port, found %d (%s)", len(webs), strings.Join(webs, ", "))
	}
	return webs[0], nil
}

// describeFromCompose fills the runtime, data and dependencies.
func describeFromCompose(app *App, s *managedapp.Spec) error {
	f, web := app.compose, app.webService()
	env, secrets, err := splitSecrets(app.web, web.Environment)
	if err != nil {
		return err
	}
	s.Runtime = managedapp.Runtime{Image: web.Image, Port: web.containerPort(), User: web.User, Env: env, Secrets: secrets}
	for vol, target := range web.namedVolumes(f.Volumes) {
		s.Data = append(s.Data, managedapp.Volume{Name: vol, Target: target})
	}
	sort.Slice(s.Data, func(i, j int) bool { return s.Data[i].Name < s.Data[j].Name })
	for _, n := range f.serviceNames() {
		if n == app.web {
			continue
		}
		svc := f.Services[n]
		if svc.Image == "" {
			return fmt.Errorf("service %s has no image; a dependency runs a pinned image", n)
		}
		denv, dsecrets, err := splitSecrets(n, svc.Environment)
		if err != nil {
			return err
		}
		dep := managedapp.Dependency{Name: n, Image: svc.Image, Env: denv, Secrets: dsecrets}
		for vol, target := range svc.namedVolumes(f.Volumes) {
			dep.Data = append(dep.Data, managedapp.Volume{Name: vol, Target: target})
		}
		sort.Slice(dep.Data, func(i, j int) bool { return dep.Data[i].Name < dep.Data[j].Name })
		if p, ok := dependencyProfiles[imageName(svc.Image)]; ok {
			dep.Ready, dep.Backup, dep.Restore = p.ready, p.backup, p.restore
		} else {
			app.Note("dependency %s (%s): no backup is known for this image; releases that need one will hold", n, imageName(svc.Image))
		}
		s.Dependencies = append(s.Dependencies, dep)
	}
	return nil
}

// splitSecrets separates configuration from secrets. A variable NAME_FILE
// pointing into /run/secrets/ becomes the secret NAME; any other variable
// whose name says it holds a secret is refused, so a value never reaches the
// Spec. Its value is never printed.
func splitSecrets(service string, env envBlock) (map[string]string, map[string]string, error) {
	plain, secrets := map[string]string{}, map[string]string{}
	var leaked []string
	for k, v := range env {
		if name, ok := strings.CutSuffix(k, "_FILE"); ok && strings.HasPrefix(v, "/run/secrets/") {
			secrets[name] = strings.TrimPrefix(v, "/run/secrets/")
			continue
		}
		if managedapp.SecretLike(k) && !managedapp.Flag(v) {
			leaked = append(leaked, k)
			continue
		}
		plain[k] = v
	}
	if len(leaked) > 0 {
		sort.Strings(leaked)
		return nil, nil, fmt.Errorf("service %s sets %s in plain environment; give each as a Compose secret (NAME_FILE=/run/secrets/name) so its value never enters the Spec",
			service, strings.Join(leaked, ", "))
	}
	return nilIfEmpty(plain), nilIfEmpty(secrets), nil
}

func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func knownStacks() string {
	names := make([]string, len(Detectors))
	for i, d := range Detectors {
		names[i] = d.Name()
	}
	return strings.Join(names, ", ")
}
