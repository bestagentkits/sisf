package install

import (
	"context"
	"github.com/bestagentkits/sisf/internal/signing"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// built is what a build produced: the pin of every unit that was built, and
// the digest the supervisor measures of its own executable.
type built struct {
	// images are the image references Compose runs (Docker mode).
	images Images
	units  map[manifest.UnitName]core.Digest
	// supervisorExe is what the supervisor's startup self-check compares to.
	supervisorExe core.Digest
	// release is the manifest signed with the root key at this up; its
	// record is the supervisor's last known good release.
	release signing.SignedManifest
}

// program is one executable of a Standalone install.
type program struct {
	file  string // name in bin/
	pkg   string // main package, relative to the source root
	units []manifest.UnitName
}

var standalonePrograms = []program{
	{"hello-app", "./examples/hello-app", []manifest.UnitName{manifest.UnitApp}},
	{"factoryd", "./cmd/factoryd", []manifest.UnitName{manifest.UnitControl, manifest.UnitDispatcher}},
	{"sisf-supervisor", "./cmd/supervisor", []manifest.UnitName{manifest.UnitSupervisor}},
	{"sisf-edge", "./cmd/edge", []manifest.UnitName{manifest.UnitEdge}},
}

// buildStandalone compiles the programs of the install into bin/ with cgo
// off, and digests each file. The supervisor's executable digest is the
// digest of its file, which is what the supervisor measures at startup.
func (in *Installer) buildStandalone(ctx context.Context) (built, error) {
	if in.SourceDir == "" {
		return built{}, invalidf("no source checkout was given to build from; run from the repository or pass --source")
	}
	if _, err := os.Stat(filepath.Join(in.SourceDir, "go.mod")); err != nil {
		return built{}, invalidf("%s is not a source checkout (no go.mod); pass --source", in.SourceDir)
	}
	if err := os.MkdirAll(filepath.Dir(in.Layout.Bin("x")), 0o755); err != nil {
		return built{}, unavailablef(err, "cannot create the bin directory")
	}
	b := built{units: map[manifest.UnitName]core.Digest{}}
	for _, p := range standalonePrograms {
		out := in.Layout.Bin(p.file)
		in.say("building %s\n", p.file)
		_, err := in.Exec.Run(ctx, Cmd{
			Name: "go", Args: []string{"build", "-trimpath", "-o", out, p.pkg}, Dir: in.SourceDir,
			Env: []string{"CGO_ENABLED=0"},
		})
		if err != nil {
			return built{}, unavailablef(err, "building %s failed", p.file)
		}
		digest, err := supervisor.DigestFile(out)
		if err != nil {
			return built{}, err
		}
		for _, u := range p.units {
			b.units[u] = digest
		}
		if p.file == "sisf-supervisor" {
			b.supervisorExe = digest
		}
	}
	return b, nil
}
