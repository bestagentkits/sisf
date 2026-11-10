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
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type mount struct {
	Type   string `json:"Type"`
	Source string `json:"Source"`
	Target string `json:"Target"`
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

// spec builds the container of an artifact.
func (b *Backend) spec(a deploy.Artifact) createSpec {
	env := append([]string{RevisionEnv + "=" + string(a.Digest)}, b.cfg.Env...)
	mounts := make([]mount, 0, len(b.cfg.Volumes))
	for _, v := range b.cfg.Volumes {
		mounts = append(mounts, mount{Type: "volume", Source: v.Name, Target: v.Target})
	}
	return createSpec{
		Image: a.Image, Env: env,
		Labels: map[string]string{LabelFactory: string(b.cfg.Factory), LabelUnit: a.Unit, LabelDigest: string(a.Digest)},
		HostConfig: hostConfig{
			NetworkMode: b.cfg.Network, RestartPolicy: restartPolicy{Name: "unless-stopped"},
			ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"}, Mounts: mounts,
		},
	}
}

// pinned reports whether an image reference names a digest, which cannot move.
func pinned(image string) bool {
	_, digest, ok := strings.Cut(image, "@")
	return ok && (strings.HasPrefix(digest, "sha256:") || strings.HasPrefix(digest, "sha512:"))
}
