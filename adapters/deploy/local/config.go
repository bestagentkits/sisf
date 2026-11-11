package local

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Fixed ports of the blue and green slots in the port table.
const (
	BluePort  = 7461
	GreenPort = 7462
)

// RevisionEnv tells the application which revision it was started as.
const RevisionEnv = "SISF_REVISION"

// StartSpec is what a Starter runs.
type StartSpec struct {
	Unit string
	// Image is the artifact's reference; what it means to run it is the
	// Starter's business.
	Image string
	// Addr is the host:port the process must listen on.
	Addr string
	// Env is the environment to add: the revision at least.
	Env []string
}

// Process is a started process.
type Process interface {
	// Alive reports whether the process is still running.
	Alive() bool
	// Stop ends the process and returns when it is gone.
	Stop(ctx context.Context) error
	// CriticalSince counts critical-level log lines written since t.
	CriticalSince(t time.Time) int64
}

// Starter starts the process of a revision.
type Starter interface {
	Start(ctx context.Context, spec StartSpec) (Process, error)
}

// Config is what a backend is built from.
type Config struct {
	Factory core.FactoryID
	// Host is the address the slots listen on; 127.0.0.1 when empty.
	Host string
	// Ports are the blue and green ports; 7461 and 7462 when zero. Tests pass
	// ephemeral ports because the fixed ones may be in use.
	Ports [2]int
	// Starter runs the processes. Required.
	Starter Starter
	// Edge divides traffic and reports what each revision served. Required.
	Edge edge.Controller
	// Env is extra environment of the unit.
	Env []string
	// HTTP probes the applications; a client with a five second timeout when nil.
	HTTP *http.Client
	// Clock times the readiness wait; the wall clock when nil.
	Clock core.Clock
	// ReadyTimeout defaults to 30 seconds and PollInterval to 500 milliseconds.
	ReadyTimeout, PollInterval time.Duration
	// Logger receives cleanup failures; nil discards them.
	Logger *slog.Logger
	// StateDir is a directory only the owner of the backend writes, where
	// it records the process of each slot so that a later backend, after a
	// restart, can stop what an earlier one left running (StopStale). Empty
	// keeps nothing on disk.
	StateDir string
}

func (c Config) withDefaults() (Config, error) {
	bad := func(msg string) (Config, error) { return c, core.NewError(core.CodeInvalidArgument, msg) }
	if err := c.Factory.Validate(); err != nil {
		return c, err
	}
	if c.Starter == nil || c.Edge == nil {
		return bad("the backend needs a starter and an edge")
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if net.ParseIP(c.Host) == nil || !net.ParseIP(c.Host).IsLoopback() {
		return bad("a standalone unit listens on a loopback address")
	}
	if c.Ports == [2]int{} {
		c.Ports = [2]int{BluePort, GreenPort}
	}
	if c.Ports[0] == c.Ports[1] || c.Ports[0] < 1 || c.Ports[1] < 1 || c.Ports[0] > 65535 || c.Ports[1] > 65535 {
		return bad("the blue and green ports must be two different valid ports")
	}
	for _, e := range c.Env {
		name, _, ok := strings.Cut(e, "=")
		if !ok || name == "" || name == RevisionEnv {
			return bad("the unit environment must be NAME=value and may not set the revision")
		}
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if c.Clock == nil {
		c.Clock = core.SystemClock{}
	}
	if c.ReadyTimeout == 0 {
		c.ReadyTimeout = 30 * time.Second
	}
	if c.PollInterval == 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.StateDir != "" && !filepath.IsAbs(c.StateDir) {
		return bad("the state directory must be an absolute path")
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

func (c Config) addr(port int) string { return net.JoinHostPort(c.Host, strconv.Itoa(port)) }
