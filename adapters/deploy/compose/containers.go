package compose

import (
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

func itoa(n int) string { return strconv.Itoa(n) }

// createSpec is the body of a container create request. It has no field for
// a bind mount, a device, a privilege or a socket, so none can be sent.
type createSpec struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	User       string            `json:"User,omitempty"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	RestartPolicy  restartPolicy     `json:"RestartPolicy"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Mounts         []mount           `json:"Mounts,omitempty"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

// containerName is derived from the factory, the unit and the digest, so one
// revision has one container however many times it is staged.
func (b *Backend) containerName(unit string, digest core.ReleaseDigest) string {
	hex := string(digest)
	if _, h, ok := strings.Cut(hex, ":"); ok {
		hex = h
	}
	return string(b.cfg.Factory) + "-" + unit + "-" + hex[:12]
}

// spec builds the container of an artifact. A bundle artifact mounts its
// bundle volume first, read-only, at the bundle target.
func (b *Backend) spec(a deploy.Artifact) createSpec {
	env := append([]string{RevisionEnv + "=" + string(a.Digest)}, b.cfg.Env...)
	mounts := make([]mount, 0, len(b.cfg.Volumes)+1)
	if a.Bundle != "" {
		mounts = append(mounts, mount{Type: "volume", Source: b.bundleVolume(a.Bundle), Target: b.cfg.BundleTarget, ReadOnly: true})
	}
	for _, v := range b.cfg.Volumes {
		mounts = append(mounts, mount{Type: "volume", Source: v.Name, Target: v.Target})
	}
	return createSpec{
		Image: a.Image, Env: env, User: b.cfg.User,
		Labels:     map[string]string{LabelFactory: string(b.cfg.Factory), LabelUnit: a.Unit, LabelDigest: string(a.Digest)},
		HostConfig: b.hostConfig(b.cfg.Network, "unless-stopped", mounts),
	}
}

// loaderSpec builds the loader of a bundle: a container of the runtime
// image, on no network and with no command, that holds the empty bundle
// volume open read-write so the bundle can be uploaded into it. It is never
// started.
func (b *Backend) loaderSpec(a deploy.Artifact) createSpec {
	return createSpec{
		Image: a.Image, User: b.cfg.User,
		Labels:     map[string]string{LabelFactory: string(b.cfg.Factory), LabelLoader: "1", LabelBundle: a.Bundle},
		HostConfig: b.hostConfig("none", "no", []mount{{Type: "volume", Source: b.bundleVolume(a.Bundle), Target: LoaderTarget}}),
	}
}

// hostConfig is what every container of the backend shares: a read-only
// root, no capability, no-new-privileges, a small scratch /tmp and the
// configured limits.
func (b *Backend) hostConfig(network, restart string, mounts []mount) hostConfig {
	return hostConfig{
		NetworkMode: network, RestartPolicy: restartPolicy{Name: restart},
		ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"}, Mounts: mounts,
		Memory: b.cfg.Limits.MemoryBytes, MemorySwap: b.cfg.Limits.MemoryBytes,
		NanoCpus: b.cfg.Limits.NanoCPUs, PidsLimit: b.cfg.Limits.Pids,
	}
}

// pinned reports whether an image reference names a digest, which cannot move.
func pinned(image string) bool {
	_, digest, ok := strings.Cut(image, "@")
	return ok && (strings.HasPrefix(digest, "sha256:") || strings.HasPrefix(digest, "sha512:"))
}
