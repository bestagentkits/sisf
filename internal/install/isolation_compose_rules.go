package install

// The rule names, the Compose fields the rules read and the table of what
// each service may have. CheckCompose (isolation_compose.go) applies them.

// Rule names of the Compose checks.
const (
	ruleImagePinned  = "image.not_pinned"
	ruleNaming       = "name.not_prefixed"
	rulePort         = "port.not_loopback"
	ruleSocket       = "socket.raw_access"
	ruleProxy        = "socket.proxy_too_open"
	ruleSecretMount  = "secret.cross_zone"
	ruleSecretInline = "secret.inline"
	ruleNetwork      = "network.unexpected"
	ruleVolume       = "volume.unexpected"
	ruleHardening    = "hardening.missing"
	ruleService      = "service.unexpected"
)

// composeFile is the part of a Compose file the isolation rules read.
type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNamed   `yaml:"networks"`
	Volumes  map[string]composeNamed   `yaml:"volumes"`
}

type composeNamed struct {
	Name     string `yaml:"name"`
	Internal bool   `yaml:"internal"`
}

type composeService struct {
	Image         string            `yaml:"image"`
	ContainerName string            `yaml:"container_name"`
	EnvFile       []string          `yaml:"env_file"`
	Environment   map[string]string `yaml:"environment"`
	Networks      []string          `yaml:"networks"`
	NetworkMode   string            `yaml:"network_mode"`
	Ports         []string          `yaml:"ports"`
	Volumes       []string          `yaml:"volumes"`
	CapAdd        []string          `yaml:"cap_add"`
	CapDrop       []string          `yaml:"cap_drop"`
	SecurityOpt   []string          `yaml:"security_opt"`
	Privileged    bool              `yaml:"privileged"`
	Pid           string            `yaml:"pid"`
	Ipc           string            `yaml:"ipc"`
}

// serviceRules is what each service may have. Anything else is a violation,
// so a service added to the template must be added here, with its reasons.
type serviceRule struct {
	envFiles []string // paths below secrets/
	networks []string
	volumes  []string // named volumes
	ports    []int
}

var serviceRules = map[string]serviceRule{
	"app":             {envFiles: []string{"application/runtime.env"}, networks: []string{"app"}, volumes: []string{"app-data"}},
	"control":         {envFiles: []string{"control/runtime.env"}, networks: []string{"control", "factory"}, volumes: []string{"control-data"}, ports: []int{PortControl, PortFakeGitHub}},
	"supervisor":      {envFiles: []string{"control/supervisor.env"}, networks: []string{"control"}, volumes: []string{"trust"}},
	"dispatcher":      {envFiles: []string{"agent/runtime.env"}, networks: []string{"factory"}},
	"edge":            {networks: []string{"app", "control", "sandbox"}, ports: []int{PortEdge}},
	"state":           {envFiles: []string{"state/runtime.env"}, networks: []string{"factory", "control"}, volumes: []string{"state-data"}, ports: []int{PortPostgres}},
	"state-bootstrap": {envFiles: []string{"state/bootstrap.env"}, networks: []string{"factory"}},
	"socket-proxy":    {networks: []string{"factory"}},
	"sim-github":      {envFiles: []string{"sim/runtime.env"}},
}

// proxyAllowed are the only socket-proxy API groups that may be on: the
// container API (create, start, inspect, logs, delete), the POST method it
// needs, and two read-only calls Docker clients make to negotiate a version.
var proxyAllowed = []string{"CONTAINERS", "POST", "PING", "VERSION"}

// proxyDenied must be switched off by name, so a default of the image that
// is "on" can never apply silently.
var proxyDenied = []string{"ALLOW_RESTARTS", "AUTH", "BUILD", "COMMIT", "CONFIGS", "DISTRIBUTION", "EVENTS", "EXEC",
	"GRPC", "IMAGES", "INFO", "NETWORKS", "NODES", "PLUGINS", "SECRETS", "SERVICES", "SESSION", "SWARM", "SYSTEM",
	"TASKS", "VOLUMES"}

// secretWords mark an environment variable that must come from an env file.
var secretWords = []string{"PASSWORD", "TOKEN", "SECRET", "DSN", "KEY", "CREDENTIAL"}
