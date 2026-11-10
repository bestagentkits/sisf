package compose

import (
	"log/slog"
	"net/http"
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
)

// RevisionEnv is the variable that tells the application which revision it
// was started as. The backend sets it; configuration cannot.
const RevisionEnv = "SISF_REVISION"

// VolumeMount is a named volume shared by every revision of the unit. A
// revision swap never touches its content.
type VolumeMount struct {
	Name   string
	Target string
}

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
)

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
