package install

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/core"
)

// DockerFilterOptions run one Docker request filter of a Docker install.
// Two of them run: the runner filter the supervisor's execution backend
// creates sandboxes through, and the deploy filter its Compose deployment
// backend runs revisions through. Each is the only Docker access of its
// one client and is reachable on the supervisor's private network only.
type DockerFilterOptions struct {
	Role      dockerfilter.Role
	Listen    string
	Socket    string
	FactoryID core.FactoryID
	// Network is the sandbox network (runner) or the deploy network (deploy).
	Network string
	Images  []string
	// SimulatedImages maps units to images (deploy role, simulation only).
	SimulatedImages map[string]string
	Logger          *slog.Logger
}

// filter builds the filter the options describe.
func (o DockerFilterOptions) filter() (*dockerfilter.Filter, error) {
	cfg := dockerfilter.Config{
		Role: o.Role, Upstream: dockerfilter.NewUnixTransport(o.Socket), RunnerValue: o.FactoryID,
		Images: o.Images, Logger: o.Logger,
	}
	switch o.Role {
	case dockerfilter.RoleRunner:
		cfg.SandboxNetwork = o.Network
		if len(o.SimulatedImages) > 0 {
			return nil, invalidf("a runner filter takes no simulated images")
		}
	case dockerfilter.RoleDeploy:
		cfg.DeployNetwork, cfg.SimulatedImages = o.Network, o.SimulatedImages
	default:
		return nil, invalidf("the filter role must be runner or deploy")
	}
	return dockerfilter.New(cfg)
}

// ServeDockerFilter serves the filter until ctx ends.
func ServeDockerFilter(ctx context.Context, o DockerFilterOptions) error {
	f, err := o.filter()
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.Listen)
	if err != nil {
		return unavailablef(err, "the Docker filter cannot listen on %s", o.Listen)
	}
	srv := &http.Server{Handler: f, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return unavailablef(err, "the Docker filter stopped serving")
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return unavailablef(err, "the Docker filter did not stop cleanly")
	}
	return nil
}
