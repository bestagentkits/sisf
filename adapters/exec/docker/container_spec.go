package docker

import (
	"encoding/json"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// workingDir resolves a command's directory inside the workspace.
func workingDir(dir string) (string, error) {
	switch {
	case dir == "":
		return workspaceDir, nil
	case path.IsAbs(dir):
		if path.Clean(dir) != dir {
			return "", invalid("the command's directory must be a clean path")
		}
		return dir, nil
	}
	joined := path.Join(workspaceDir, dir)
	if joined != workspaceDir && !strings.HasPrefix(joined, workspaceDir+"/") {
		return "", invalid("the command's directory must stay inside the workspace")
	}
	return joined, nil
}

// containerRequest is the configuration of the container one command runs
// in: the sandbox's image, user, limits and network, with the hardening the
// filter requires, the proxy variables and the run's secrets.
func (b *Backend) containerRequest(sb *sandbox, cmd execution.Command, secretEnv map[string]string) (dockerfilter.CreateRequest, error) {
	dir, err := workingDir(cmd.Dir)
	if err != nil {
		return dockerfilter.CreateRequest{}, err
	}
	if err := checkEnv(cmd.Env); err != nil {
		return dockerfilter.CreateRequest{}, err
	}
	env := map[string]string{}
	for _, layer := range []map[string]string{sb.spec.Env, cmd.Env} {
		for k, v := range layer {
			env[k] = v
		}
	}
	for k, v := range secretEnv {
		if _, taken := env[k]; taken {
			return dockerfilter.CreateRequest{}, invalid("environment variable " + k + " is given twice")
		}
		env[k] = v
	}
	if proxy := b.cfg.EgressProxy; proxy != "" {
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[name] = proxy
		}
	}
	list := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		list = append(list, k+"="+env[k])
	}
	labels := map[string]string{"sisf.run_id": string(sb.spec.RunID), "sisf.unit": sb.spec.Unit, "sisf.sandbox": string(sb.info.ID)}
	for k, v := range sb.spec.Labels {
		labels[k] = v
	}
	for k, v := range b.labels {
		labels[k] = v
	}
	scratch := "rw,exec,nosuid,nodev,size=" + strconv.FormatInt(sb.limits.DiskBytes, 10)
	stop := 5
	return dockerfilter.CreateRequest{
		Image: sb.spec.Image, Cmd: cmd.Argv, Env: list, Labels: labels, WorkingDir: dir, User: b.cfg.User, StopTimeout: &stop,
		HostConfig: dockerfilter.HostConfig{
			NetworkMode: b.cfg.Network, ReadonlyRootfs: true, CapDrop: []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"}, Runtime: sb.runtime,
			Memory: sb.limits.MemoryBytes, MemorySwap: sb.limits.MemoryBytes, NanoCpus: sb.limits.CPUMillis * 1_000_000,
			PidsLimit: sb.limits.PIDs, Tmpfs: map[string]string{workspaceDir: scratch, "/tmp": scratch}, Init: true,
		},
		NetworkingConfig: &dockerfilter.NetworkingConfig{EndpointsConfig: map[string]struct{}{b.cfg.Network: {}}},
	}, nil
}

// appliedHost is the part of an inspected host configuration the backend
// checks before it starts a container.
type appliedHost struct {
	NetworkMode    string            `json:"NetworkMode"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Privileged     bool              `json:"Privileged"`
	CapAdd         []string          `json:"CapAdd"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Runtime        string            `json:"Runtime"`
	Memory         int64             `json:"Memory"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      *int64            `json:"PidsLimit"`
	Binds          []string          `json:"Binds"`
	Mounts         []json.RawMessage `json:"Mounts"`
	VolumesFrom    []string          `json:"VolumesFrom"`
	Devices        []json.RawMessage `json:"Devices"`
	PidMode        string            `json:"PidMode"`
	IpcMode        string            `json:"IpcMode"`
	PortBindings   map[string]any    `json:"PortBindings"`
}

// verifyApplied checks that the daemon created the container as asked: a
// runner of this factory with every part of the hardening in place.
func (b *Backend) verifyApplied(view containerView, want dockerfilter.CreateRequest) error {
	raw, err := json.Marshal(view.HostConfig)
	if err != nil {
		return core.NewError(core.CodeInternal, "the container's configuration cannot be read back").WithCause(err)
	}
	var h appliedHost
	if err := json.Unmarshal(raw, &h); err != nil {
		return core.NewError(core.CodeInternal, "the container's configuration cannot be read back").WithCause(err)
	}
	w := want.HostConfig
	ok := view.Config.Labels[b.cfg.RunnerLabel] == string(b.cfg.FactoryID) &&
		h.NetworkMode == w.NetworkMode && h.ReadonlyRootfs && !h.Privileged && len(h.CapAdd) == 0 &&
		slices.Contains(h.CapDrop, "ALL") && slices.Contains(h.SecurityOpt, "no-new-privileges") &&
		(w.Runtime == "" || h.Runtime == w.Runtime) && h.Memory == w.Memory && h.NanoCpus == w.NanoCpus &&
		h.PidsLimit != nil && *h.PidsLimit == w.PidsLimit && len(h.Binds) == 0 && len(h.Mounts) == 0 &&
		len(h.VolumesFrom) == 0 && len(h.Devices) == 0 && (h.PidMode == "" || h.PidMode == "private") &&
		h.IpcMode != "host" && len(h.PortBindings) == 0
	if !ok {
		return core.NewError(core.CodeInternal, "the daemon did not apply the sandbox hardening; the container was removed unstarted")
	}
	return nil
}
