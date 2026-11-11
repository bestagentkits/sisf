package install

// The rule names, the Compose fields the rules read and the table of what
// each service may have. CheckCompose (isolation_compose.go) applies them.

import "gopkg.in/yaml.v3"

// Rule names of the Compose checks.
const (
	ruleImagePinned  = "image.not_pinned"
	ruleNaming       = "name.not_prefixed"
	rulePort         = "port.not_loopback"
	ruleSocket       = "socket.raw_access"
	ruleProxy        = "socket.proxy_present"
	ruleSecretMount  = "secret.cross_zone"
	ruleSecretInline = "secret.inline"
	ruleNetwork      = "network.unexpected"
	ruleVolume       = "volume.unexpected"
	ruleHardening    = "hardening.missing"
	ruleService      = "service.unexpected"
)

// composeFile is a Compose file as this deployment writes it. The file is
// decoded strictly, so a key that is not listed here, at any level, makes it
// unreadable and refused. The keys the template writes are listed, and so
// are those that would widen a zone's reach, so that each of them is refused
// with a reason of its own rather than as an unknown key.
type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
	Volumes  map[string]composeVolume  `yaml:"volumes"`
	// Hardened is the template's block of hardening settings, merged into
	// the services; what it sets is checked there.
	Hardened yaml.Node `yaml:"x-hardened"`
	// Configs and Secrets would put host files into containers. The
	// deployment declares none: each zone reads its secrets from the env
	// file of its own area.
	Configs map[string]yaml.Node `yaml:"configs"`
	Secrets map[string]yaml.Node `yaml:"secrets"`
}

type composeNetwork struct {
	Name     string `yaml:"name"`
	Internal bool   `yaml:"internal"`
}

type composeVolume struct {
	Name string `yaml:"name"`
	// Driver and DriverOpts would make a named volume something else, such
	// as a bind of a host directory (type: none, o: bind, device: <path>).
	Driver     string               `yaml:"driver"`
	DriverOpts map[string]yaml.Node `yaml:"driver_opts"`
	// Labels name the install that made the volume; they change nothing
	// about what the volume is.
	Labels map[string]string `yaml:"labels"`
}

type composeService struct {
	Image         string                       `yaml:"image"`
	ContainerName string                       `yaml:"container_name"`
	Command       yaml.Node                    `yaml:"command"`
	Restart       string                       `yaml:"restart"`
	Profiles      []string                     `yaml:"profiles"`
	DependsOn     map[string]composeDependency `yaml:"depends_on"`
	Healthcheck   composeHealthcheck           `yaml:"healthcheck"`
	EnvFile       []string                     `yaml:"env_file"`
	Environment   map[string]string            `yaml:"environment"`
	Networks      networkList                  `yaml:"networks"`
	NetworkMode   string                       `yaml:"network_mode"`
	Ports         []string                     `yaml:"ports"`
	Volumes       []string                     `yaml:"volumes"`
	ReadOnly      bool                         `yaml:"read_only"`
	Tmpfs         []string                     `yaml:"tmpfs"`
	CapDrop       []string                     `yaml:"cap_drop"`
	SecurityOpt   []string                     `yaml:"security_opt"`
	// Labels name the release of the app container; GroupAdd gives the
	// Docker request filters the socket's group.
	Labels   map[string]string `yaml:"labels"`
	GroupAdd []string          `yaml:"group_add"`

	// Settings the template never writes, each refused with its reason.
	CapAdd      []string    `yaml:"cap_add"`
	Privileged  bool        `yaml:"privileged"`
	Devices     []yaml.Node `yaml:"devices"`
	VolumesFrom []string    `yaml:"volumes_from"`
	Pid         string      `yaml:"pid"`
	Ipc         string      `yaml:"ipc"`
	Uts         string      `yaml:"uts"`
	Cgroup      string      `yaml:"cgroup"`
	UsernsMode  string      `yaml:"userns_mode"`
	Configs     []yaml.Node `yaml:"configs"`
	Secrets     []yaml.Node `yaml:"secrets"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test        []string `yaml:"test"`
	Interval    string   `yaml:"interval"`
	Timeout     string   `yaml:"timeout"`
	Retries     int      `yaml:"retries"`
	StartPeriod string   `yaml:"start_period"`
}

// confinementOption is the one security_opt a service may set. Any other
// changes the confinement Docker applies by default, such as
// seccomp:unconfined, apparmor:unconfined or label:disable.
const confinementOption = "no-new-privileges:true"

// serviceRules is what each service may have. Anything else is a violation,
// so a service added to the template must be added here, with its reasons.
type serviceRule struct {
	envFiles []string // paths below secrets/
	networks []string
	volumes  []string // named volumes
	ports    []int
	// networkMode is the one network_mode the service may set instead of
	// networks: another service's namespace, or none.
	networkMode string
	// aliases are the network aliases it may have, by network.
	aliases map[string][]string
	// socket marks the two Docker request filters, the only services that
	// mount the Docker socket, read-only and nothing else of the host.
	socket bool
	// simAgent lets the dispatcher mount the scripted agent's directory.
	simAgent bool
}

// The services that mount the Docker socket, and the network only they and
// the supervisor are on.
const (
	filterRunner  = "docker-filter-runner"
	filterDeploy  = "docker-filter-deploy"
	dockerNetwork = "docker"
)

var serviceRules = map[string]serviceRule{
	"app": {envFiles: []string{"application/runtime.env"}, networks: []string{"app"}, volumes: []string{"app-data"}},
	// Control shares the supervisor's admin socket volume, and in the
	// simulation publishes the fake GitHub and the fake identity provider
	// that run in its network namespace.
	"control": {envFiles: []string{"control/runtime.env"}, networks: []string{"control", "factory"}, volumes: []string{"control-data", supervisorAdminVolume},
		ports: []int{PortControl, PortFakeGitHub, PortFakeOIDC}},
	// The supervisor reaches GitHub and the edge's admin listener (control),
	// the store and the dispatcher's run control (factory), the revisions it
	// deploys (app) and its two filters (docker); it serves its admin socket
	// on the volume it shares with control alone.
	"supervisor":      {envFiles: []string{"control/supervisor.env"}, networks: []string{"control", "factory", "app", dockerNetwork}, volumes: []string{"trust", supervisorAdminVolume}},
	"trust-bootstrap": {envFiles: []string{"control/" + trustEnvFile}, volumes: []string{"trust"}, networkMode: "none"},
	"dispatcher":      {envFiles: []string{"agent/runtime.env"}, networks: []string{"factory"}, simAgent: true},
	"edge": {envFiles: []string{"edge/runtime.env"}, networks: []string{"app", "control", "sandbox"}, ports: []int{PortEdge},
		aliases: map[string][]string{"control": {edgeAdminAlias}, "sandbox": {egressAlias}}},
	"state":            {envFiles: []string{"state/runtime.env"}, networks: []string{"factory"}, volumes: []string{"state-data"}, ports: []int{PortPostgres}},
	"state-bootstrap":  {envFiles: []string{"state/bootstrap.env"}, networks: []string{"factory"}},
	"sim-github":       {envFiles: []string{"sim/runtime.env"}, networkMode: "service:control"},
	"sim-oidc":         {networkMode: "service:control"},
	"sim-github-relay": {networkMode: "service:supervisor"},
	filterRunner:       {networks: []string{dockerNetwork}, socket: true},
	filterDeploy:       {networks: []string{dockerNetwork}, socket: true},
}

// supervisorAdminVolume holds the supervisor's admin socket; only the
// supervisor and control mount it.
const supervisorAdminVolume = "supervisor-admin"

// Aliases of the edge's two inner listeners. Each is bound to the address
// the alias resolves to, so it is reachable on that one network only.
const (
	edgeAdminAlias = "edge-admin"
	egressAlias    = "egress"
)

// socketMount is the one mount of the Docker socket a filter has.
const socketMount = dockerSocket + ":" + dockerSocket + ":ro"

// dockerSocketNames are file names of a container runtime's API socket. A
// mount whose path contains one of them hands the holder the host.
var dockerSocketNames = []string{"docker.sock", "podman.sock", "containerd.sock"}

// socketProxyWords mark a service or image as a Docker socket proxy.
var socketProxyWords = []string{"socket-proxy", "socketproxy", "docker-proxy", "dockerproxy"}

// secretWords mark an environment variable that must come from an env file.
var secretWords = []string{"PASSWORD", "TOKEN", "SECRET", "DSN", "KEY", "CREDENTIAL"}
