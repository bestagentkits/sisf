package main

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func init() {
	register(Command{
		Path:    []string{"docker-filter"},
		Args:    "--role runner|deploy --listen host:port --factory-id id --network name --image ref... [--socket path] [--sim-image unit=ref]... [--app-spec path [--app-volume name=volume]... [--app-secrets-volume volume]]",
		Summary: "Serve a Docker request filter in front of the Docker socket",
		Help: `
Serves the Docker request filter of one role in front of the Docker socket:

  runner  the runner containers candidate builds run in, on the sandbox network
  deploy  the revisions of the factory's units, on the deploy network

A Docker install's Compose project starts both roles itself. For the Docker
build sandbox of a Standalone install, the operator starts the runner role
before ` + "`sisf up`" + `, on a loopback address, and gives that address to
` + "`sisf init`" + ` with --exec-docker-endpoint (see ` + "`sisf help init`" + `).

Everything the role does not need is refused. --image lists the allowed
images, each pinned by digest or named by its local image ID. --sim-image is
for the deploy role of the simulation profile only.

--app-spec admits, in the deploy role, the revisions of a managed application:
its runtime image, its bundle mounted read-only, its environment and its data
volumes, each bound to a volume of the install with --app-volume, and its
secrets volume, read-only. The supervisor is given the same flags, so both
derive one configuration from the Spec.`,
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
	var appSpec string
	var binding managedapp.Binding
	fs.StringVar(&appSpec, "app-spec", "", "deploy role: absolute path of the managed application's installed "+managedapp.FileName)
	fs.Func("app-volume", "deploy role, with --app-spec: name=volume binding of a data volume (repeatable)", binding.BindVolume)
	fs.StringVar(&binding.Secrets, "app-secrets-volume", "", "deploy role, with --app-spec: the volume holding the runtime's secrets as files")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if o.Listen == "" || factoryID == "" || o.Network == "" || (len(images) == 0 && appSpec == "") {
		return usagef("--listen, --factory-id, --network and --image (or --app-spec) are required")
	}
	if appSpec == "" && (len(binding.Volumes) > 0 || binding.Secrets != "") {
		return usagef("--app-volume and --app-secrets-volume need --app-spec")
	}
	if appSpec != "" {
		app, err := managedapp.LoadInstall(appSpec, binding)
		if err != nil {
			return err
		}
		o.App = &app.Revision
	}
	o.Role, o.FactoryID, o.Images, o.SimulatedImages = dockerfilter.Role(role), core.FactoryID(factoryID), images, sims
	if err := env.Printf("docker filter (%s) of %s serving on %s\n", role, factoryID, o.Listen); err != nil {
		return err
	}
	return install.ServeDockerFilter(ctx, o)
}
