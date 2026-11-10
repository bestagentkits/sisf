package install

import (
	"bytes"
	"encoding/json"
	"regexp"
	"text/template"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/templates"
)

// Images names the image of each program Docker mode runs. Each is a digest
// pin, either "repo@sha256:..." of a pushed image or the "sha256:..." ID of
// an image built on this host; neither can move.
type Images struct {
	Factoryd, Supervisor, Edge, App, Sisf string
	// Runner and QARunner are the sandboxes candidate builds and QA run in.
	Runner, QARunner string
}

func (i Images) all() []string {
	return []string{i.Factoryd, i.Supervisor, i.Edge, i.App, i.Sisf, i.Runner, i.QARunner}
}

// ComposeData is what the Compose template is filled with. Nothing in it is
// secret: secrets are reached through env files that the Compose CLI reads on
// the host, and only their paths appear here.
type ComposeData struct {
	Project    string
	FactoryID  core.FactoryID
	Repository string
	// SecretsDir, ConfigFile, ReleaseFile and SimAgentDir are absolute host
	// paths.
	SecretsDir, ConfigFile, ReleaseFile, SimAgentDir string
	GitHubAPIURL                                     string
	Images                                           Images
	Sim, Debug                                       bool
	// BootstrapCommand is the command of the one-shot state step, as a
	// JSON array (which is also a YAML flow sequence).
	BootstrapCommand string
	// AppDigest is the release of the application `sisf up` installs, and
	// AppContainer the name the supervisor's deployment backend gives its
	// container, so the backend observes it as the last known good release.
	AppDigest, AppContainer string
	Host                    HostFacts
	Lifecycle               LifecycleChoice

	PostgresImage                                                  string
	PortEdge, PortApp, PortControl, PortSupervisor, PortDispatcher int
	PortFakeGitHub, PortPostgres, PortEgress, PortEdgeAdmin        int
	PortDockerFilter, PortDispatcherAdmin                          int
}

// newComposeData fills the fixed parts of the data for an install.
func newComposeData(l Layout, o Options, images Images, bootstrap []string, host HostFacts) (ComposeData, error) {
	cmd, err := json.Marshal(bootstrap)
	if err != nil {
		return ComposeData{}, invalidf("the bootstrap command cannot be encoded")
	}
	proj := project(o.FactoryID)
	if proj != string(o.FactoryID) {
		// The supervisor names the containers it deploys after the factory,
		// so the factory's name must already carry the project's prefix.
		return ComposeData{}, invalidf("Docker mode needs a factory id that starts with sisf- and has no dot, not %q", o.FactoryID)
	}
	app := digestOf(images.App)
	return ComposeData{
		Project: proj, FactoryID: o.FactoryID, Repository: o.Repo.Owner + "/" + o.Repo.Repo,
		SecretsDir: l.Dir + "/secrets", ConfigFile: l.Config(), ReleaseFile: l.ReleaseManifest(), SimAgentDir: l.SimAgentDir(),
		GitHubAPIURL: o.GitHubAPIURL, Images: images, Sim: o.Sim, Debug: o.Debug,
		BootstrapCommand: string(cmd), PostgresImage: PostgresImage,
		AppDigest: string(app), AppContainer: deployedContainer(o.FactoryID, "app", app),
		Host: host, Lifecycle: lifecycleChoice(o, manifest.ModeDocker),
		PortEdge: PortEdge, PortApp: PortApp, PortControl: PortControl, PortSupervisor: PortSupervisor,
		PortDispatcher: PortDispatcher, PortFakeGitHub: PortFakeGitHub, PortPostgres: PortPostgres,
		PortEgress: PortEgress, PortEdgeAdmin: PortEdgeAdmin, PortDockerFilter: PortDockerFilter,
		PortDispatcherAdmin: PortDispatcherAdmin,
	}, nil
}

// pathPattern is what an installation path may contain when it is written
// into a template: no space, quote, newline or other character a YAML, XML
// or unit file would read as syntax.
var pathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./+-]*$`)

// check refuses data that would let a value break out of its place in the file.
func (d ComposeData) check() error {
	for _, p := range []string{d.SecretsDir, d.ConfigFile, d.ReleaseFile, d.SimAgentDir} {
		if !pathPattern.MatchString(p) {
			return invalidf("the install path %q has characters Compose files cannot carry; use letters, digits and . _ + - /", p)
		}
	}
	if err := checkGitHubURL(d.GitHubAPIURL); err != nil {
		return err
	}
	for _, image := range d.Images.all() {
		if !digestPinned(image) {
			return invalidf("image %q is not pinned by digest", image)
		}
	}
	if err := d.Host.check(); err != nil {
		return err
	}
	return d.Lifecycle.check()
}

// RenderCompose fills the Compose template. It refuses images that are not
// pinned by digest, so a rendered file never runs a name that can move.
func RenderCompose(d ComposeData) ([]byte, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	tpl, err := template.New("compose.yaml").Option("missingkey=error").ParseFS(templates.FS, "compose/compose.yaml")
	if err != nil {
		return nil, invalidf("the Compose template cannot be read: %v", err)
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "compose.yaml", d); err != nil {
		return nil, invalidf("the Compose template cannot be filled: %v", err)
	}
	return out.Bytes(), nil
}
