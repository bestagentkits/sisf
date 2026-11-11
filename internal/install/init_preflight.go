package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
)

// foreignEntries are files a directory may already hold and still receive
// an install: the stored CLI login lives in the SISF home, and Finder
// leaves its own file everywhere.
var foreignEntries = []string{"credentials.json", ".DS_Store"}

// Preflight checks everything `sisf init` would refuse, and changes nothing:
// the options, the target directory, the source checkout every `sisf up`
// builds from, and that the factory could start with the manifest init
// would write. The command line calls it before it asks the Owner anything,
// so nobody decides about money for an install that is then refused.
func (in *Installer) Preflight(o Options) (Options, error) {
	o, err := o.normalize()
	if err != nil {
		return o, err
	}
	l := in.Layout
	if l.Dir != o.Dir {
		return o, invalidf("the options and the installer name different directories")
	}
	if done, err := l.installed(); err != nil {
		return o, unavailablef(err, "cannot inspect %s", l.Dir)
	} else if done {
		return o, core.NewError(core.CodeAlreadyExists, "an install already exists in "+l.Dir,
			core.NextAction{Action: "sisf up --dir " + l.Dir, Description: "start the existing install"})
	}
	if err := checkEmptyTarget(l.Dir); err != nil {
		return o, err
	}
	if err := in.checkSource(); err != nil {
		return o, err
	}
	m := buildManifest(o)
	if err := conformance.Check(m).Err(); err != nil {
		return o, err
	}
	if err := checkStartable(m); err != nil {
		return o, err
	}
	return o, nil
}

// checkEmptyTarget refuses a directory that holds anything init did not
// make: init would mix the factory's secrets with someone's files, and a
// failed init could not tell its own files from theirs when it cleans up.
func checkEmptyTarget(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return unavailablef(err, "cannot read the install directory %s", dir)
	}
	var others []string
	for _, e := range entries {
		if !slices.Contains(foreignEntries, e.Name()) {
			others = append(others, e.Name())
		}
	}
	if len(others) == 0 {
		return nil
	}
	shown := others[:min(len(others), 3)]
	return invalidf("the install directory %s already holds %d other entries (%s); choose a new or empty directory with --dir",
		dir, len(others), strings.Join(shown, ", "))
}

// sourceModule is the module of the checkout `sisf up` builds from.
const sourceModule = "module github.com/bestagentkits/sisf"

// FindSource returns the source checkout that holds start: start itself or
// the nearest parent whose go.mod is this project's, or "" when there is
// none. It lets `sisf init` and `sisf up` run from any directory of the
// checkout.
func FindSource(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if strings.TrimSpace(line) == sourceModule {
					return dir
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// checkSource refuses an install whose source checkout cannot be built
// from: every `sisf up` builds the factory's programs or images from it.
func (in *Installer) checkSource() error {
	if in.SourceDir == "" {
		return invalidf("no source checkout of sisf was found: run sisf from the checkout, or pass --source with its path; every `sisf up` builds the factory from it")
	}
	if !filepath.IsAbs(in.SourceDir) {
		return invalidf("the source checkout %s must be an absolute path", in.SourceDir)
	}
	if _, err := os.Stat(filepath.Join(in.SourceDir, "go.mod")); err != nil {
		return invalidf("%s is not a source checkout of sisf (no go.mod); pass --source with the checkout's path", in.SourceDir)
	}
	return nil
}

// createdPaths records what one init created, so a failure removes exactly
// that: the directories it made, up to the first that existed, and every
// entry it added to a directory that existed.
type createdPaths struct {
	top      string   // the highest directory init made, "" when Dir existed
	existing []string // the entries Dir held before, kept on failure
}

// beginCreate notes what exists before init writes anything in dir.
func beginCreate(dir string) createdPaths {
	var c createdPaths
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		c.top = d
		if filepath.Dir(d) == d {
			break
		}
	}
	if c.top == "" {
		entries, _ := os.ReadDir(dir) // a directory that cannot be read was refused by Preflight
		for _, e := range entries {
			c.existing = append(c.existing, e.Name())
		}
	}
	return c
}

// undo removes what init created. It is best effort: init already failed,
// and that failure is what the Owner is told.
func (c createdPaths) undo(dir string) {
	if c.top != "" {
		_ = os.RemoveAll(c.top)
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !slices.Contains(c.existing, e.Name()) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}
