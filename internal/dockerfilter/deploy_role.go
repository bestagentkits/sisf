package dockerfilter

// The deploy role. The supervisor's Compose deployment backend runs each
// revision of a unit as a container of its own, and reaches Docker only
// through a filter in this role. What passes:
//
//   - GET or HEAD /vX/_ping.
//   - GET /vX/containers/json, only with a label filter naming this
//     factory, so the list holds the factory's units and nothing else.
//   - POST /vX/containers/create, only for a container named
//     <factory>-<unit>-<first 12 hex of its digest>, labelled exactly
//     sisf.factory, sisf.unit and sisf.digest for that name, from an
//     allowed unit image, on the deploy network alone, read-only, with every
//     capability dropped, no-new-privileges, and as its only environment the
//     revision it is started as. No mount, volume, device, port, privilege,
//     host namespace or other setting can be sent.
//   - inspect, start, stop, logs and remove of a container that carries the
//     factory's unit labels and is not a runner.
//
// Everything else, and every endpoint of the runner role that a deployment
// does not need (wait, info), is refused.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Labels of a deployed revision; they match the deployment backend's.
const (
	DeployLabelFactory = "sisf.factory"
	DeployLabelUnit    = "sisf.unit"
	DeployLabelDigest  = "sisf.digest"
	// DeployRevisionEnv is the only environment variable a revision gets.
	DeployRevisionEnv = "SISF_REVISION"
)

// DefaultDeployAPIVersions are the API versions of the deploy role when the
// configuration names none: the deployment backend's default, 1.43, and the
// runner role's.
var DefaultDeployAPIVersions = append([]string{"1.43"}, DefaultAPIVersions...)

var (
	deployUnitPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	deployDigestPattern = regexp.MustCompile(`^sha256:([a-f0-9]{64})$`)
	restartPolicies     = []string{"", "no", "unless-stopped"}
)

// settledDeploy applies the defaults of the deploy role and checks it.
func (c Config) settledDeploy() (Config, error) {
	if c.UpstreamURL == "" {
		c.UpstreamURL = "http://docker"
	}
	if len(c.APIVersions) == 0 {
		c.APIVersions = slices.Clone(DefaultDeployAPIVersions)
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.Logger == nil {
		c.Logger = slogDefault()
	}
	switch {
	case c.Upstream == nil:
		return c, invalid("the filter needs a transport to the Docker daemon")
	case !plainBaseURL(c.UpstreamURL):
		return c, invalid("the upstream URL must be a plain http or https base URL")
	case c.SandboxNetwork != "" || c.RunnerLabel != "" || c.NamePrefix != "" || len(c.Runtimes) > 0:
		return c, invalid("a deploy filter takes no sandbox network, runner label, name prefix or runtimes")
	case !networkNamePattern.MatchString(c.DeployNetwork) || reservedNetworks[c.DeployNetwork]:
		return c, invalid("the deploy network must be a user-defined network name")
	case len(c.Images) == 0:
		return c, invalid("the filter needs at least one unit image")
	case c.MaxBodyBytes < 1024:
		return c, invalid("the body limit is too small for a create request")
	}
	if err := c.RunnerValue.Validate(); err != nil {
		return c, err
	}
	for unit, image := range c.SimulatedImages {
		if !deployUnitPattern.MatchString(unit) || !slices.Contains(c.Images, image) {
			return c, invalid("a simulated image must name a unit and one of the allowed images")
		}
	}
	return c, c.checkLists()
}

// deployCreateRequest is the only container configuration the deploy role
// forwards; its shape is the deployment backend's.
type deployCreateRequest struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	User       string            `json:"User,omitempty"`
	HostConfig deployHostConfig  `json:"HostConfig"`
}

type deployHostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	RestartPolicy  restartPolicy     `json:"RestartPolicy"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

var (
	deployCreateKeys = []string{"Image", "Env", "Labels", "User", "HostConfig"}
	deployHostKeys   = []string{"NetworkMode", "RestartPolicy", "ReadonlyRootfs", "CapDrop", "SecurityOpt", "Tmpfs"}
)

// createDeploy checks a create request of the deploy role and forwards the
// filter's own encoding of what it checked.
func (f *Filter) createDeploy(w http.ResponseWriter, r *http.Request, rt route) *denial {
	raw, d := f.readBody(r)
	if d != nil {
		return d
	}
	body, err := f.checkDeployCreate(rt.query.Get("name"), raw)
	if err != nil {
		return deny(http.StatusForbidden, err.Error())
	}
	return f.forward(w, r, http.MethodPost, rt.version, "/containers/create", map[string][]string{"name": {rt.query.Get("name")}}, body)
}

func (f *Filter) checkDeployCreate(name string, raw []byte) ([]byte, error) {
	if err := scanStrict(raw); err != nil {
		return nil, err
	}
	top, err := objectKeys(raw, "the container", deployCreateKeys...)
	if err != nil {
		return nil, err
	}
	hostRaw, ok := top["HostConfig"]
	if !ok {
		return nil, errors.New("a deployed revision needs a HostConfig")
	}
	host, err := objectKeys(hostRaw, "HostConfig", deployHostKeys...)
	if err != nil {
		return nil, err
	}
	if policy, ok := host["RestartPolicy"]; ok {
		if _, err := objectKeys(policy, "RestartPolicy", "Name"); err != nil {
			return nil, err
		}
	}
	var req deployCreateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, errors.New("the container configuration has a field of the wrong type")
	}
	if err := f.checkDeployRequest(name, &req); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

// checkDeployRequest judges a decoded deploy configuration. A simulated
// candidate's image is replaced here by the image its unit runs as.
func (f *Filter) checkDeployRequest(name string, req *deployCreateRequest) error {
	unit, digest := req.Labels[DeployLabelUnit], req.Labels[DeployLabelDigest]
	hex := deployDigestPattern.FindStringSubmatch(digest)
	switch {
	case len(req.Labels) != 3 || req.Labels[DeployLabelFactory] != string(f.cfg.RunnerValue):
		return errors.New("a deployed revision carries exactly this factory's sisf.factory, sisf.unit and sisf.digest labels")
	case !deployUnitPattern.MatchString(unit) || hex == nil:
		return errors.New("the unit or digest label is malformed")
	case name != string(f.cfg.RunnerValue)+"-"+unit+"-"+hex[1][:12]:
		return errors.New("the container name must be the factory, the unit and the digest of the revision")
	case !slices.Equal(req.Env, []string{DeployRevisionEnv + "=" + digest}):
		return errors.New("a deployed revision's only environment is the revision it is started as")
	case req.User != "" && checkUser(req.User) != nil:
		return errors.New("a deployed revision runs as its image's user or a numeric user other than root")
	}
	if sim, ok := f.cfg.SimulatedImages[unit]; ok && req.Image == "sim.local/"+string(f.cfg.RunnerValue)+"/"+unit+"@"+digest {
		f.log.Info("simulated candidate runs as its unit's image", "unit", unit, "digest", digest)
		req.Image = sim
	}
	if !f.images[req.Image] {
		return fmt.Errorf("image %q is not an allowed unit image", req.Image)
	}
	h := req.HostConfig
	switch {
	case h.NetworkMode != f.cfg.DeployNetwork:
		return fmt.Errorf("network mode %q is not the deploy network", h.NetworkMode)
	case !slices.Contains(restartPolicies, h.RestartPolicy.Name):
		return fmt.Errorf("restart policy %q is not allowed", h.RestartPolicy.Name)
	case !h.ReadonlyRootfs:
		return errors.New("a deployed revision needs a read-only root file system")
	case !slices.ContainsFunc(h.CapDrop, func(c string) bool { return strings.EqualFold(c, "ALL") }):
		return errors.New("a deployed revision must drop every capability")
	}
	if err := checkSecurityOpt(h.SecurityOpt); err != nil {
		return err
	}
	if err := checkStrings("CapDrop", h.CapDrop, 64); err != nil {
		return err
	}
	return checkTmpfs(h.Tmpfs)
}

// deployed reports whether labels mark a revision of this factory's units.
func (f *Filter) deployed(labels map[string]string) bool {
	_, runner := labels[DefaultRunnerLabel]
	return !runner && labels[DeployLabelFactory] == string(f.cfg.RunnerValue) &&
		deployUnitPattern.MatchString(labels[DeployLabelUnit]) && deployDigestPattern.MatchString(labels[DeployLabelDigest])
}

// list forwards a container list whose label filter names this factory, so
// the daemon answers with the factory's containers only.
func (f *Filter) list(w http.ResponseWriter, r *http.Request, rt route) *denial {
	raw := rt.query.Get("filters")
	if raw == "" {
		return deny(http.StatusForbidden, "a container list must filter by this factory's label")
	}
	if err := scanStrict([]byte(raw)); err != nil {
		return deny(http.StatusBadRequest, "the list filter is not plain JSON")
	}
	var filters map[string][]string
	if err := json.Unmarshal([]byte(raw), &filters); err != nil || len(filters) != 1 || len(filters["label"]) == 0 || len(filters["label"]) > 8 {
		return deny(http.StatusForbidden, "a container list may filter by labels only")
	}
	if !slices.Contains(filters["label"], DeployLabelFactory+"="+string(f.cfg.RunnerValue)) {
		return deny(http.StatusForbidden, "a container list must filter by this factory's label")
	}
	for _, l := range filters["label"] {
		key, _, _ := strings.Cut(l, "=")
		if !labelKeyPattern.MatchString(key) || len(l) > 256 || strings.ContainsFunc(l, isControl) {
			return deny(http.StatusForbidden, "label filter "+strconv.Quote(l)+" is not allowed")
		}
	}
	return f.forward(w, r, http.MethodGet, rt.version, "/containers/json", rt.query, nil)
}

// isSinceTime is a Unix time in seconds, as the logs endpoint takes it.
func isSinceTime(v string) bool {
	n, err := strconv.ParseInt(v, 10, 64)
	return err == nil && n >= 0 && n < 1<<40
}

// deployQueryRules are the parameters of each endpoint of the deploy role.
var deployQueryRules = map[endpoint]queryRule{
	endpointCreate:  {"name": func(string) bool { return true }}, // checked against the labels
	endpointInspect: {"size": isFalse},
	endpointStop:    {"t": isSmallCount},
	endpointLogs:    {"stdout": isBool, "stderr": isBool, "timestamps": isBool, "tail": isTail, "since": isSinceTime},
	endpointRemove:  {"force": isBool, "v": isFalse},
	endpointList:    {"all": isBool, "filters": func(string) bool { return true }}, // checked by list
}

// deployEndpoints are the endpoints the deploy role serves.
var deployEndpoints = map[endpoint]bool{
	endpointPing: true, endpointCreate: true, endpointInspect: true, endpointStart: true,
	endpointStop: true, endpointLogs: true, endpointRemove: true, endpointList: true,
}
