package dockerfilter

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Defaults of the filter.
const (
	// DefaultRunnerLabel is the label key every runner container carries;
	// its value is the factory ID.
	DefaultRunnerLabel = "sisf.runner"
	// DefaultNamePrefix starts the name of every runner container. A fixed
	// prefix keeps a runner from taking the name, and with it the network
	// alias, of another service on the sandbox network.
	DefaultNamePrefix = "sisf-runner-"
	// DefaultMaxBodyBytes bounds a create request body.
	DefaultMaxBodyBytes = 64 << 10
	// LabelPrefix starts every label a runner container may carry, so that
	// no label can be read by another tool, such as a reverse proxy that
	// routes traffic by container label, as an instruction.
	LabelPrefix = "sisf."
)

// DefaultAPIVersions are the Docker API versions the filter accepts when the
// configuration names none: Docker Engine 25 to 28.
var DefaultAPIVersions = []string{"1.44", "1.45", "1.46", "1.47", "1.48", "1.49", "1.50", "1.51"}

// DefaultRuntimes are the container runtimes a runner may ask for. An empty
// runtime, the daemon's default, is always allowed.
var DefaultRuntimes = []string{"runc", "runsc"}

// Config is what a filter is built from.
type Config struct {
	// Upstream carries requests to the Docker daemon, usually
	// NewUnixTransport over the daemon's socket.
	Upstream http.RoundTripper
	// UpstreamURL is the base URL requests are sent to through Upstream;
	// "http://docker" when empty, which suits a socket transport.
	UpstreamURL string
	// RunnerLabel is the label key of runner containers;
	// DefaultRunnerLabel when empty. It must start with LabelPrefix.
	RunnerLabel string
	// RunnerValue is the value the runner label must have: the factory ID.
	RunnerValue core.FactoryID
	// SandboxNetwork is the only network a runner may join.
	SandboxNetwork string
	// Images are the runner images allowed, each pinned by digest.
	Images []string
	// APIVersions are the API versions accepted; DefaultAPIVersions when empty.
	APIVersions []string
	// Runtimes are the container runtimes a runner may ask for;
	// DefaultRuntimes when empty.
	Runtimes []string
	// NamePrefix starts every runner container name; DefaultNamePrefix when empty.
	NamePrefix string
	// MaxBodyBytes bounds a create body; DefaultMaxBodyBytes when zero.
	MaxBodyBytes int64
	// Logger receives one line per refusal; slog.Default when nil.
	Logger *slog.Logger
}

var (
	networkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	digestImagePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._/:-][a-z0-9]+)*@sha256:[a-f0-9]{64}$`)
	versionPattern     = regexp.MustCompile(`^1\.[0-9]{1,2}$`)
	runtimeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	labelKeyPattern    = regexp.MustCompile(`^sisf\.[a-z0-9][a-z0-9_.-]{0,62}$`)
	namePrefixPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
)

// settled returns the configuration with defaults applied, or
// INVALID_ARGUMENT when a part is missing or unsafe.
func (c Config) settled() (Config, error) {
	if c.RunnerLabel == "" {
		c.RunnerLabel = DefaultRunnerLabel
	}
	if c.UpstreamURL == "" {
		c.UpstreamURL = "http://docker"
	}
	if len(c.APIVersions) == 0 {
		c.APIVersions = slices.Clone(DefaultAPIVersions)
	}
	if len(c.Runtimes) == 0 {
		c.Runtimes = slices.Clone(DefaultRuntimes)
	}
	if c.NamePrefix == "" {
		c.NamePrefix = DefaultNamePrefix
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	base, err := url.Parse(c.UpstreamURL)
	switch {
	case c.Upstream == nil:
		return c, invalid("the filter needs a transport to the Docker daemon")
	case err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || strings.Trim(base.Path, "/") != "":
		return c, invalid("the upstream URL must be a plain http or https base URL")
	case !labelKeyPattern.MatchString(c.RunnerLabel):
		return c, invalid("the runner label must be a lowercase key starting with " + LabelPrefix)
	case !namePrefixPattern.MatchString(strings.TrimSuffix(c.NamePrefix, "-")) || !strings.HasSuffix(c.NamePrefix, "-"):
		return c, invalid("the runner name prefix must be lowercase and end with a dash")
	case !networkNamePattern.MatchString(c.SandboxNetwork) || reservedNetworks[c.SandboxNetwork]:
		return c, invalid("the sandbox network must be a user-defined network name")
	case len(c.Images) == 0:
		return c, invalid("the filter needs at least one runner image")
	case c.MaxBodyBytes < 1024:
		return c, invalid("the body limit is too small for a create request")
	}
	if err := c.RunnerValue.Validate(); err != nil {
		return c, err
	}
	for _, image := range c.Images {
		if !digestImagePattern.MatchString(image) {
			return c, invalid("runner image " + image + " is not pinned by digest")
		}
	}
	for _, v := range c.APIVersions {
		if !versionPattern.MatchString(v) {
			return c, invalid("API version " + v + " is not of the form 1.NN")
		}
	}
	for _, r := range c.Runtimes {
		if !runtimeNamePattern.MatchString(r) {
			return c, invalid("runtime " + r + " is not a runtime name")
		}
	}
	return c, nil
}

// reservedNetworks are the daemon's own networks and namespace modes; none
// of them is a sandbox.
var reservedNetworks = map[string]bool{"host": true, "bridge": true, "none": true, "default": true}

// NewUnixTransport returns a transport that sends every request to the
// Docker daemon listening on the unix socket at path, whatever host the
// request names.
func NewUnixTransport(path string) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", path)
		},
		Proxy:                 nil,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 0, // wait and logs answer when the container exits
		DisableCompression:    true,
	}
}

func invalid(message string) *core.Error { return core.NewError(core.CodeInvalidArgument, message) }
