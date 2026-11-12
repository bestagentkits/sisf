package install

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// image is one image Docker mode builds.
type image struct {
	name       string // suffix of the local tag
	dockerfile string // below build/
	units      []manifest.UnitName
}

var dockerImages = []image{
	{"hello-app", "hello-app.Dockerfile", []manifest.UnitName{manifest.UnitApp}},
	{"factoryd", "factoryd.Dockerfile", []manifest.UnitName{manifest.UnitControl, manifest.UnitDispatcher}},
	{"supervisor", "supervisor.Dockerfile", []manifest.UnitName{manifest.UnitSupervisor}},
	{"edge", "edge.Dockerfile", []manifest.UnitName{manifest.UnitEdge}},
	{"sisf", "sisf.Dockerfile", nil},
}

// buildDocker builds the images of the install and pins each by content.
// An image that was pushed to a registry is pinned by "repo@sha256:...";
// one built here has no registry digest, so it is pinned by its image ID,
// which Docker resolves locally and which names the same bytes forever.
func (in *Installer) buildDocker(ctx context.Context, proj string) (built, error) {
	if in.SourceDir == "" {
		return built{}, invalidf("no source checkout was given to build from; run from the repository or pass --source")
	}
	b := built{units: map[manifest.UnitName]core.Digest{manifest.UnitState: PostgresDigest}}
	refs := map[string]string{}
	for _, img := range dockerImages {
		tag := proj + "-" + img.name + ":build"
		in.say("building image %s\n", tag)
		if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
			"build", "--quiet", "-f", filepath.Join(in.SourceDir, "build", img.dockerfile), "-t", tag, in.SourceDir,
		}}); err != nil {
			return built{}, unavailablef(err, "building image %s failed", img.name)
		}
		ref, err := in.pinImage(ctx, tag)
		if err != nil {
			return built{}, err
		}
		refs[img.name] = ref
		for _, u := range img.units {
			b.units[u] = digestOf(ref)
		}
	}
	b.images = Images{Factoryd: refs["factoryd"], Supervisor: refs["supervisor"], Edge: refs["edge"], App: refs["hello-app"], Sisf: refs["sisf"]}

	// The supervisor measures its own executable, so ask the image, with
	// nothing mounted and no network.
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"run", "--rm", "--name", proj + "-measure", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--entrypoint", "supervisor",
		b.images.Supervisor, "digest",
	}})
	if err != nil {
		return built{}, unavailablef(err, "the supervisor image could not measure itself")
	}
	b.supervisorExe = core.Digest(strings.TrimSpace(string(out)))
	if err := b.supervisorExe.Validate(); err != nil {
		return built{}, invalidf("the supervisor image printed no digest")
	}
	return b, nil
}

// pinImage returns the reference that pins a built image by content.
func (in *Installer) pinImage(ctx context.Context, tag string) (string, error) {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"image", "inspect", "--format", "{{.Id}} {{join .RepoDigests \" \"}}", tag,
	}})
	if err != nil {
		return "", unavailablef(err, "image %s cannot be inspected", tag)
	}
	fields := strings.Fields(string(out))
	switch {
	case len(fields) > 1 && digestPinned(fields[1]):
		return fields[1], nil
	case len(fields) > 0 && digestPinned(fields[0]):
		return fields[0], nil
	}
	return "", invalidf("image %s has no digest to pin it by", tag)
}

// digestOf returns the digest part of a pinned image reference.
func digestOf(ref string) core.Digest {
	if _, d, ok := strings.Cut(ref, "@"); ok {
		return core.Digest(d)
	}
	return core.Digest(ref)
}
