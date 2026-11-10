package dockerfilter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// CreateRequest is the only container configuration the filter forwards.
// The filter decodes a client's body into it after checking the raw keys,
// checks the values, and sends its own encoding of it, so the daemon reads
// no field the filter did not judge. The docker execution adapter builds
// its requests from the same type.
type CreateRequest struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	User       string            `json:"User"`
	// StopTimeout is how many seconds a stop waits before it kills.
	StopTimeout      *int              `json:"StopTimeout,omitempty"`
	HostConfig       HostConfig        `json:"HostConfig"`
	NetworkingConfig *NetworkingConfig `json:"NetworkingConfig,omitempty"`
}

// HostConfig is the part of the host configuration a runner may state.
// Every field the daemon knows and this type lacks (binds, mounts, volumes,
// devices, host namespaces, published ports, log drivers, sysctls, ...) is
// refused when a client sends it.
type HostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Privileged     bool              `json:"Privileged"`
	CapAdd         []string          `json:"CapAdd"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Runtime        string            `json:"Runtime,omitempty"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Init           bool              `json:"Init,omitempty"`
}

// NetworkingConfig may name the sandbox network, with no settings.
type NetworkingConfig struct {
	EndpointsConfig map[string]struct{} `json:"EndpointsConfig"`
}

var (
	createKeys     = []string{"Image", "Cmd", "Entrypoint", "Env", "Labels", "WorkingDir", "User", "StopTimeout", "HostConfig", "NetworkingConfig"}
	hostConfigKeys = []string{"NetworkMode", "ReadonlyRootfs", "Privileged", "CapAdd", "CapDrop", "SecurityOpt", "Runtime",
		"Memory", "MemorySwap", "NanoCpus", "PidsLimit", "Tmpfs", "Init"}
	noNewPrivileges = []string{"no-new-privileges", "no-new-privileges:true", "no-new-privileges=true"}
	envNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	tmpfsOption     = regexp.MustCompile(`^(rw|ro|exec|noexec|nosuid|nodev|size=[0-9]{1,15}[kmg]?|mode=[0-7]{3,4})$`)
)

// Bounds of a create body's lists.
const (
	maxArgs       = 256
	maxEnv        = 256
	maxLabels     = 32
	maxTmpfs      = 8
	maxValueBytes = 32 << 10
)

// checkCreate judges a raw create body and returns the body to forward.
func (f *Filter) checkCreate(raw []byte) ([]byte, error) {
	if err := scanStrict(raw); err != nil {
		return nil, err
	}
	top, err := objectKeys(raw, "the container", createKeys...)
	if err != nil {
		return nil, err
	}
	hostRaw, ok := top["HostConfig"]
	if !ok {
		return nil, errors.New("a runner needs a HostConfig with its limits")
	}
	if _, err := objectKeys(hostRaw, "HostConfig", hostConfigKeys...); err != nil {
		return nil, err
	}
	if netRaw, ok := top["NetworkingConfig"]; ok {
		if err := f.checkNetworking(netRaw); err != nil {
			return nil, err
		}
	}
	var req CreateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, errors.New("the container configuration has a field of the wrong type")
	}
	if err := f.CheckCreateRequest(req); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

// checkNetworking allows only the sandbox network, with no endpoint
// settings: no aliases, no fixed addresses, no links, no driver options.
func (f *Filter) checkNetworking(raw json.RawMessage) error {
	fields, err := objectKeys(raw, "NetworkingConfig", "EndpointsConfig")
	if err != nil {
		return err
	}
	var endpoints map[string]json.RawMessage
	if err := json.Unmarshal(fields["EndpointsConfig"], &endpoints); err != nil {
		return errors.New("EndpointsConfig must be a JSON object")
	}
	for name, settings := range endpoints {
		if name != f.cfg.SandboxNetwork {
			return fmt.Errorf("network %q is not the sandbox network", name)
		}
		if _, err := objectKeys(settings, "the sandbox endpoint"); err != nil {
			return err
		}
	}
	return nil
}

// CheckCreateRequest judges a decoded configuration: the image, the labels,
// the user, the command and the whole host configuration.
func (f *Filter) CheckCreateRequest(req CreateRequest) error {
	if !f.images[req.Image] {
		return fmt.Errorf("image %q is not an allowed runner image", req.Image)
	}
	if err := f.checkLabels(req.Labels); err != nil {
		return err
	}
	if err := checkUser(req.User); err != nil {
		return err
	}
	if err := checkStrings("Cmd", req.Cmd, maxArgs); err != nil {
		return err
	}
	if err := checkStrings("Entrypoint", req.Entrypoint, maxArgs); err != nil {
		return err
	}
	if err := checkEnv(req.Env); err != nil {
		return err
	}
	switch {
	case req.WorkingDir != "" && (!path.IsAbs(req.WorkingDir) || path.Clean(req.WorkingDir) != req.WorkingDir):
		return errors.New("the working directory must be a clean absolute path")
	case req.StopTimeout != nil && (*req.StopTimeout < 0 || *req.StopTimeout > 3600):
		return errors.New("the stop timeout is out of range")
	}
	if n := req.NetworkingConfig; n != nil {
		for name := range n.EndpointsConfig {
			if name != f.cfg.SandboxNetwork {
				return fmt.Errorf("network %q is not the sandbox network", name)
			}
		}
	}
	return f.checkHost(req.HostConfig)
}

func (f *Filter) checkLabels(labels map[string]string) error {
	if labels[f.cfg.RunnerLabel] != string(f.cfg.RunnerValue) {
		return errors.New("the container does not carry this factory's runner label")
	}
	if len(labels) > maxLabels {
		return errors.New("the container carries too many labels")
	}
	for key, value := range labels {
		if !labelKeyPattern.MatchString(key) {
			return fmt.Errorf("label %q is outside the %s namespace", key, LabelPrefix)
		}
		if len(value) > 256 || strings.ContainsFunc(value, isControl) {
			return fmt.Errorf("label %q has a value that is not allowed", key)
		}
	}
	return nil
}

// numericUser is uid or uid:gid, neither of them 0. A user name is refused
// because the image's own passwd file could map any name to root.
var numericUser = regexp.MustCompile(`^[1-9][0-9]{0,9}(:[1-9][0-9]{0,9})?$`)

// checkUser refuses root and every user given by name.
func checkUser(user string) error {
	if !numericUser.MatchString(user) {
		return errors.New("a runner must run as a numeric user and group other than root")
	}
	return nil
}

func checkStrings(what string, values []string, limit int) error {
	if len(values) > limit {
		return fmt.Errorf("%s has too many entries", what)
	}
	for _, v := range values {
		if len(v) > maxValueBytes || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%s has an entry that is not allowed", what)
		}
	}
	return nil
}

func checkEnv(env []string) error {
	if err := checkStrings("Env", env, maxEnv); err != nil {
		return err
	}
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !envNamePattern.MatchString(name) {
			return errors.New("environment entries must be NAME=value")
		}
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
