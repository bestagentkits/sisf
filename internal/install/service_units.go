package install

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/bestagentkits/sisf/templates"
)

// unitData fills the launchd and systemd templates. Every path is absolute
// and matches quotedPathPattern; the templates quote them where a space would
// split them.
type unitData struct {
	Label, FactoryID string
	// Sisf is the installed sisf program the unit runs.
	Sisf string
	// Dir is the install directory and Source the checkout every `sisf up`
	// builds from; the unit also starts in Source.
	Dir, Source string
	// Path is the search path of the unit: the directories of the programs
	// `sisf up` runs, which a service manager's own minimal PATH lacks.
	Path string
}

// systemPath ends the search path of every unit.
var systemPath = []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// lookPath finds a program the way the unit's `sisf up` will.
func (in *Installer) lookPath(name string) (string, error) {
	if in.LookPath != nil {
		return in.LookPath(name)
	}
	return exec.LookPath(name)
}

// temporaryBuild reports whether path is a program `go run` built into its
// temporary directory, which Go deletes when the command ends.
func temporaryBuild(path string) bool {
	return strings.Contains(path, string(filepath.Separator)+"go-build") && filepath.Base(filepath.Dir(path)) == "exe"
}

// ServiceUnitsBlocker says why no service unit can be written that would
// really start this install, or "" when one can. A unit that runs a deleted
// program, or a `sisf up` that cannot find Go to build with, would fail at
// every start.
func (in *Installer) ServiceUnitsBlocker() string {
	switch {
	case in.Sisf == "" || !filepath.IsAbs(in.Sisf):
		return "the path of this sisf program is unknown"
	case temporaryBuild(in.Sisf):
		return "this sisf runs from a temporary `go run` build, which Go deletes when it ends; build it with `go build -o <dir>/sisf ./cmd/sisf`, then run `<dir>/sisf service units --dir " + in.Layout.Dir + "`"
	}
	if _, err := in.lookPath("go"); err != nil {
		return "`go` is not on PATH, and every `sisf up` builds the factory with it; install Go, then run `sisf service units --dir " + in.Layout.Dir + "`"
	}
	for _, p := range []string{in.Sisf, in.Layout.Dir, in.SourceDir} {
		if !quotedPathPattern.MatchString(p) {
			return "the path " + p + " has characters a service unit cannot carry (a quote, %, $, \\ or a double space)"
		}
	}
	return ""
}

// unitSearchPath is the PATH of the units: the directories of go, git and
// gh and of the real agents the install runs, as found now, then the
// system's.
func (in *Installer) unitSearchPath() string {
	var dirs []string
	for _, name := range []string{"go", "git", "gh", "claude", "codex"} {
		if p, err := in.lookPath(name); err == nil && filepath.IsAbs(p) {
			if d := filepath.Dir(p); quotedPathPattern.MatchString(d) && !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	for _, d := range systemPath {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, ":")
}

// writeServiceUnits renders the service-manager units into the install's
// service directory and creates the log directory the launchd unit writes
// to. They are only generated: copying one into place and enabling it is
// the Owner's step, so nothing outside the install directory changes. When
// no unit could start the factory, none is written and the note says why
// and what to do.
func (in *Installer) writeServiceUnits(o Options) (note string, err error) {
	if why := in.ServiceUnitsBlocker(); why != "" {
		return "Service units were not written: " + why + ".", nil
	}
	data := unitData{Label: "sh.sisf." + string(o.FactoryID), FactoryID: string(o.FactoryID), Sisf: in.Sisf,
		Dir: o.Dir, Source: in.SourceDir, Path: in.unitSearchPath()}
	for _, dir := range []string{in.Layout.ServiceDir(), in.Layout.Logs()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", unavailablef(err, "cannot create %s", dir)
		}
	}
	for _, f := range []struct{ src, dst string }{
		{"standalone/sisf.service", data.Label + ".service"},
		{"standalone/sisf.plist", data.Label + ".plist"},
	} {
		tpl, err := template.New("unit").Option("missingkey=error").ParseFS(templates.FS, f.src)
		if err != nil {
			return "", invalidf("the service template %s cannot be read: %v", f.src, err)
		}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, filepath.Base(f.src), data); err != nil {
			return "", invalidf("the service template %s cannot be filled: %v", f.src, err)
		}
		if err := os.WriteFile(filepath.Join(in.Layout.ServiceDir(), f.dst), out.Bytes(), 0o644); err != nil {
			return "", unavailablef(err, "cannot write %s", f.dst)
		}
	}
	return "", nil
}

// WriteServiceUnits writes the service units of an existing Standalone
// install, for when init could not, or after sisf was moved or rebuilt.
func (in *Installer) WriteServiceUnits() error {
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return err
	}
	o, err := in.installedOptions(m)
	if err != nil {
		return err
	}
	if o.Mode != "standalone" {
		return invalidf("the install in %s runs in %s mode; only a Standalone install has service units, Compose restarts the containers itself", in.Layout.Dir, o.Mode)
	}
	if err := in.checkSource(); err != nil {
		return err
	}
	if why := in.ServiceUnitsBlocker(); why != "" {
		return invalidf("no service unit was written: %s", why)
	}
	if _, err := in.writeServiceUnits(o); err != nil {
		return err
	}
	return writeSourceRecord(in.Layout, in.SourceDir)
}
