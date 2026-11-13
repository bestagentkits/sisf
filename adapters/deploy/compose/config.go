package compose

import (
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Labels the backend puts on every container it creates.
const (
	LabelFactory = "sisf.factory"
	LabelUnit    = "sisf.unit"
	LabelDigest  = "sisf.digest"
	// LabelLoader marks the container that loads a bundle volume; it is
	// never started.
	LabelLoader = "sisf.loader"
	// LabelBundle names the bundle digest a loader or a bundle volume holds.
	LabelBundle = "sisf.bundle"
)

// LoaderTarget is where a loader mounts the bundle volume it fills, and the
// only path a bundle is uploaded at. The deploy role of the Docker request
// filter allows no other.
const LoaderTarget = "/sisf-bundle"

// RevisionEnv is the variable that tells the application which revision it
// was started as. The backend sets it; configuration cannot.
const RevisionEnv = "SISF_REVISION"

// VolumeMount is a named volume shared by every revision of the unit. A
// revision swap never touches its content.
type VolumeMount struct {
	Name   string
	Target string
}

// ResourceLimits bound one revision's container.
type ResourceLimits struct {
	// MemoryBytes is also the memory-and-swap limit: a revision never swaps.
	MemoryBytes int64
	// NanoCPUs is in billionths of a CPU.
	NanoCPUs int64
	Pids     int64
}

// DefaultResourceLimits give a revision 512 MiB of memory, one CPU and 512
// processes; the deploy role of the Docker request filter refuses a
// revision without limits or above its maxima.
var DefaultResourceLimits = ResourceLimits{MemoryBytes: 512 << 20, NanoCPUs: 1_000_000_000, Pids: 512}

// Config is what a backend is built from.
type Config struct {
	// Endpoint is the supervisor-only Docker endpoint, unix:///path or
	// http://host:port. It is required and has no default.
	Endpoint string
	// APIVersion of the Docker API; 1.43 when empty.
	APIVersion string
	// Factory names the deployment; an artifact built for another is refused.
	Factory core.FactoryID
	// Network is the Compose network the unit's containers join.
	Network string
	// AppPort is the port the application listens on inside its container.
	AppPort int
	// Env is extra environment of the unit, for example its data directory.
	Env []string
	// Volumes are mounted into every revision.
	Volumes []VolumeMount
	// User is the numeric uid:gid every revision runs as, neither of them
	// 0; the image's user when empty.
	User string
	// BundleTarget is where a revision of a bundle artifact mounts its
	// content bundle, read-only. BundleSource yields the verified tar of a
	// bundle. A backend without both refuses a bundle artifact.
	BundleTarget string
	BundleSource BundleSource
	// Limits bound each revision's container; DefaultResourceLimits fills
	// a zero field.
	Limits ResourceLimits
	// Edge divides traffic and reports what each revision served. Required.
	Edge edge.Controller
	// Resolve maps a container name to the base URL where the backend and the
	// edge reach it; http://name:AppPort when nil.
	Resolve func(container string) string
	// HTTP probes the applications; a client with a five second timeout when nil.
	HTTP *http.Client
	// Clock times the readiness wait; the wall clock when nil.
	Clock core.Clock
	// ReadyTimeout bounds how long a staged revision may take to report
	// itself healthy; 60 seconds when zero. PollInterval is the step, 1 second when zero.
	ReadyTimeout, PollInterval time.Duration
	// Logger receives cleanup failures; nil discards them.
	Logger *slog.Logger
}

var (
	unitPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	networkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	envPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	userPattern    = regexp.MustCompile(`^[1-9][0-9]{0,9}:[1-9][0-9]{0,9}$`)
)

// cleanTarget is a clean absolute path below the root.
func cleanTarget(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && p != "/"
}

// withDefaults fills the optional fields and checks the required ones.
func (c Config) withDefaults() (Config, error) {
	bad := func(msg string) (Config, error) { return c, core.NewError(core.CodeInvalidArgument, msg) }
	if err := c.Factory.Validate(); err != nil {
		return c, err
	}
	if !networkPattern.MatchString(c.Network) {
		return bad("the Compose network name is missing or malformed")
	}
	if c.AppPort < 1 || c.AppPort > 65535 {
		return bad("the application port must be 1 to 65535")
	}
	if c.Edge == nil {
		return bad("the backend needs an edge to divide traffic and read counters")
	}
	for _, e := range c.Env {
		name, _, _ := strings.Cut(e, "=")
		if !envPattern.MatchString(e) || name == RevisionEnv || strings.HasPrefix(name, "DOCKER_") {
			return bad("the unit environment must be NAME=value and may not set the revision or a Docker variable")
		}
	}
	for _, v := range c.Volumes {
		if !networkPattern.MatchString(v.Name) || !strings.HasPrefix(v.Target, "/") || v.Target == "/" || strings.Contains(v.Target, "..") {
			return bad("a volume needs a named volume and an absolute target path")
		}
		if strings.HasPrefix(v.Name, string(c.Factory)+"-bundle-") || v.Target == c.BundleTarget {
			return bad("a data volume may not be named like a bundle volume or take the bundle's place")
		}
	}
	if c.User != "" && !userPattern.MatchString(c.User) {
		return bad("a revision runs as a numeric uid:gid other than root")
	}
	if c.BundleTarget != "" && (!cleanTarget(c.BundleTarget) || c.BundleTarget == LoaderTarget) {
		return bad("the bundle target must be a clean absolute path below / other than the loader's")
	}
	if c.Limits.MemoryBytes < 0 || c.Limits.NanoCPUs < 0 || c.Limits.Pids < 0 {
		return bad("the resource limits of a revision must be positive")
	}
	if c.Limits.MemoryBytes == 0 {
		c.Limits.MemoryBytes = DefaultResourceLimits.MemoryBytes
	}
	if c.Limits.NanoCPUs == 0 {
		c.Limits.NanoCPUs = DefaultResourceLimits.NanoCPUs
	}
	if c.Limits.Pids == 0 {
		c.Limits.Pids = DefaultResourceLimits.Pids
	}
	if c.APIVersion == "" {
		c.APIVersion = "1.43"
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if c.Clock == nil {
		c.Clock = core.SystemClock{}
	}
	if c.ReadyTimeout == 0 {
		c.ReadyTimeout = 60 * time.Second
	}
	if c.PollInterval == 0 {
		c.PollInterval = time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.Resolve == nil {
		port := itoa(c.AppPort)
		c.Resolve = func(container string) string { return "http://" + container + ":" + port }
	}
	return c, nil
}
