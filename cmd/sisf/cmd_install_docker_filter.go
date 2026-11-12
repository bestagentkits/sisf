package main

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
)

func init() {
	register(Command{
		Path:    []string{"docker-filter"},
		Args:    "--role runner|deploy --listen host:port --factory-id id --network name --image ref... [--socket path] [--sim-image unit=ref]...",
		Summary: "Serve a Docker request filter of a Docker install",
		Help: `
Run by the Compose project of a Docker install, never by hand. Serves the
Docker request filter of one role in front of the Docker socket:

  runner  the runner containers candidate builds run in, on the sandbox network
  deploy  the revisions of the factory's units, on the deploy network

Everything the role does not need is refused. --image lists the allowed
images, each pinned by digest or named by its local image ID. --sim-image is
for the deploy role of the simulation profile only.`,
		Run: runDockerFilter,
	})
}

// imageList collects repeated --image flags.
type imageList []string

func (l *imageList) String() string     { return strings.Join(*l, ",") }
func (l *imageList) Set(v string) error { *l = append(*l, v); return nil }

// simImages collects repeated --sim-image unit=ref flags.
type simImages map[string]string

func (s simImages) String() string { return "" }
func (s simImages) Set(v string) error {
	unit, ref, ok := strings.Cut(v, "=")
	if !ok || unit == "" || ref == "" {
		return usagef("--sim-image must be unit=ref")
	}
	s[unit] = ref
	return nil
}

func runDockerFilter(ctx context.Context, env *Env, args []string) error {
	var o install.DockerFilterOptions
	var role, factoryID string
	var images imageList
	sims := simImages{}
	fs := newFlagSet("docker-filter")
	fs.StringVar(&role, "role", "", "runner or deploy")
	fs.StringVar(&o.Listen, "listen", "", "address to serve on")
	fs.StringVar(&o.Socket, "socket", "/var/run/docker.sock", "the Docker daemon's socket")
	fs.StringVar(&factoryID, "factory-id", "", "factory the containers belong to")
	fs.StringVar(&o.Network, "network", "", "the sandbox network (runner) or the deploy network (deploy)")
	fs.Var(&images, "image", "allowed image (repeatable)")
	fs.Var(sims, "sim-image", "deploy role, simulation only: unit=ref (repeatable)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if o.Listen == "" || factoryID == "" || o.Network == "" || len(images) == 0 {
		return usagef("--listen, --factory-id, --network and --image are required")
	}
	o.Role, o.FactoryID, o.Images, o.SimulatedImages = dockerfilter.Role(role), core.FactoryID(factoryID), images, sims
	if err := env.Printf("docker filter (%s) of %s serving on %s\n", role, factoryID, o.Listen); err != nil {
		return err
	}
	return install.ServeDockerFilter(ctx, o)
}
