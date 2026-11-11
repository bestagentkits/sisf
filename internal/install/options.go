package install

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Fixed ports of the deployment. They are the ports the plan reserves for
// this project; nothing else is ever bound, and always on 127.0.0.1.
const (
	PortEdge       = 7460
	PortApp        = 7461
	PortControl    = 7463
	PortSupervisor = 7464
	PortDispatcher = 7465
	PortFakeGitHub = 7467
	PortPostgres   = 7469
)

// Defaults of a fresh install.
const (
	// DefaultGitHubAPI is the public GitHub REST API.
	DefaultGitHubAPI = "https://api.github.com"
	// SimRepository is the repository the fake GitHub starts with.
	SimRepository = "sisf-sim/hello-app"
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	slugPattern  = regexp.MustCompile(`[^a-z0-9]+`)
)

// Options is what `sisf init` needs to know. The zero value of every field
// but Dir and Mode has a safe default.
type Options struct {
	// Dir is the absolute install directory.
	Dir  string
	Mode manifest.Mode
	// FactoryID and DeploymentID default from the repository and the mode.
	FactoryID    core.FactoryID
	DeploymentID core.DeploymentID
	// Repo is the GitHub repository the factory serves; the simulation
	// profile defaults it to SimRepository.
	Repo manifest.Repository
	// Sim runs against the fake GitHub instead of the real one.
	Sim bool
	// GitHubToken is the installation token. Under Sim a random stand-in is
	// generated, since the fake accepts any token.
	GitHubToken core.SecretValue
	// GitHubAPIURL overrides the API root. It must be https, or http on a
	// loopback host.
	GitHubAPIURL string
	// Budget is the Owner's install-time confirmation; the zero value means
	// not confirmed, and the factory then accepts intake but claims nothing.
	Budget BudgetChoice
	// QAAgent and QAAgentBinary configure the agent judge of a production
	// install: the QA agent, and in Docker mode the absolute path of the
	// launcher that runs it in a sandbox. They come from the manifest's
	// extensions qa_agent and qa_agent_binary.
	QAAgent, QAAgentBinary string
	// BuildCommand is the command, split on spaces, the supervisor runs to
	// build a candidate; from the manifest's extension build_command. In
	// Docker mode the stages are off without it.
	BuildCommand string
	// Agents are the real coding and QA agents of a Standalone install.
	Agents AgentOptions
	// BuildSandbox is the Docker sandbox a Standalone install builds
	// candidates in; the zero value builds with the local backend, which
	// builds nothing an agent wrote.
	BuildSandbox BuildSandbox
	// Identity is how people sign in to the control zone.
	Identity Identity
	// Dev is a development install: Standalone on loopback only, signing in
	// at the fake identity provider so no person has to click.
	Dev bool
	// GitHubCredential "gh-cli" takes the GitHub token from the local gh
	// login (`gh auth token`) at init and at every start; development
	// installs only.
	GitHubCredential string
	// Debug publishes the state store on 127.0.0.1:7469 (Docker mode only).
	Debug bool
	// ReconcileInterval is how often control reconciles with GitHub; zero
	// keeps control's default. A development install on the operator's gh
	// login takes no webhooks and learns of issues only this way.
	ReconcileInterval time.Duration
}

// normalize validates the options and fills the defaults.
func (o Options) normalize() (Options, error) {
	if !filepath.IsAbs(o.Dir) {
		return o, invalidf("the install directory must be an absolute path")
	}
	o.Dir = filepath.Clean(o.Dir)
	if !o.Mode.Valid() {
		return o, invalidf("mode must be %s or %s", manifest.ModeStandalone, manifest.ModeDocker)
	}
	if o.Debug && o.Mode != manifest.ModeDocker {
		return o, invalidf("--debug publishes the state store, which only Docker mode has")
	}
	if o.Repo == (manifest.Repository{}) && o.Sim {
		o.Repo.Owner, o.Repo.Repo, _ = strings.Cut(SimRepository, "/")
	}
	if !ownerPattern.MatchString(o.Repo.Owner) || !namePattern.MatchString(o.Repo.Repo) || o.Repo.Repo == "." || o.Repo.Repo == ".." {
		return o, invalidf("the repository must be given as owner/name")
	}
	if o.FactoryID == "" {
		slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(o.Repo.Repo), "-"), "-")
		o.FactoryID = core.FactoryID("sisf-" + slug)
	}
	if o.DeploymentID == "" {
		o.DeploymentID = core.DeploymentID("local-" + string(o.Mode))
	}
	if err := o.FactoryID.Validate(); err != nil {
		return o, err
	}
	if err := o.DeploymentID.Validate(); err != nil {
		return o, err
	}
	if o.GitHubAPIURL == "" {
		o.GitHubAPIURL = DefaultGitHubAPI
		if o.Sim {
			o.GitHubAPIURL = "http://127.0.0.1:" + strconv.Itoa(PortFakeGitHub)
		}
	}
	if err := checkGitHubURL(o.GitHubAPIURL); err != nil {
		return o, err
	}
	if err := o.checkDev(); err != nil {
		return o, err
	}
	id, err := o.Identity.normalize(o.Sim || o.Dev)
	if err != nil {
		return o, err
	}
	o.Identity = id
	if o.Agents, err = o.Agents.normalize(o); err != nil {
		return o, err
	}
	if err := o.BuildSandbox.normalize(o); err != nil {
		return o, err
	}
	if err := checkReconcileInterval(o.ReconcileInterval); err != nil {
		return o, err
	}
	if err := o.Budget.Validate(); err != nil {
		return o, err
	}
	o.Budget = o.Budget.withDefaults()
	return o, nil
}

// checkGitHubURL requires https, or plain http to a loopback host, so a
// token is never sent in clear text across a network.
func checkGitHubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return invalidf("the GitHub API URL is not a valid URL")
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return invalidf("the GitHub API URL must be https, or http on a loopback address")
	}
	return nil
}

// project is the Compose project name and the prefix of every container,
// network and volume of this install. "sisf-" marks them as this tool's.
func project(id core.FactoryID) string {
	name := strings.NewReplacer(".", "-").Replace(string(id))
	if strings.HasPrefix(name, "sisf-") {
		return name
	}
	return "sisf-" + name
}
