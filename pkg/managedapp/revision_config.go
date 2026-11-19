package managedapp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// HealthPath is the only health path a revision is probed at: the
// supervisor's health probe and the deploy backend read it there.
const HealthPath = "/healthz"

// volumeNamePattern is the shape of a Docker named volume.
var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Binding names, for one install, the Docker volumes the Owner made for the
// Spec: Volumes maps each data volume's name in the Spec to its volume, and
// Secrets is the volume whose files hold the runtime's secrets. A Spec is
// committed to the application's repository; the volumes are the install's.
type Binding struct {
	Volumes map[string]string
	Secrets string
}

// Mount is one named volume a revision mounts.
type Mount struct {
	Name, Target string
	ReadOnly     bool
}

// RevisionConfig is what every revision of a managed application is
// started with. The deploy backend that starts a revision and the Docker
// request filter that admits it are both configured from it, so the two
// cannot disagree on an entry.
type RevisionConfig struct {
	Image        string
	Port         int
	User         string
	Env          []string
	BundleTarget string
	// Mounts are the data volumes in the Spec's order, writable, then the
	// secrets volume at SecretsDir, read-only.
	Mounts []Mount
}

// Revision derives the revision of the Spec under binding. A binding that
// leaves a data volume or the secrets unbound, binds a volume the Spec
// does not have, or binds one volume twice is refused, as is a runtime the
// deploy backend cannot run as written: a command, or a health path other
// than HealthPath.
func (s *Spec) Revision(b Binding) (RevisionConfig, error) {
	bad := func(format string, args ...any) (RevisionConfig, error) {
		return RevisionConfig{}, core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
	}
	r := s.Runtime
	if len(r.Command) > 0 {
		return RevisionConfig{}, core.NewError(core.CodeNotSupported, "the deploy backend runs the image's own command; runtime.command is not supported")
	}
	if r.HealthPath != HealthPath {
		return RevisionConfig{}, core.NewError(core.CodeNotSupported, "the deploy backend probes a revision at "+HealthPath+"; runtime.health_path must be "+HealthPath)
	}
	rev := RevisionConfig{Image: r.Image, Port: r.Port, User: r.User, Env: r.RevisionEnv(), BundleTarget: s.Bundle.Mount}
	used := map[string]bool{}
	use := func(volume string) error {
		switch {
		case !volumeNamePattern.MatchString(volume):
			return fmt.Errorf("%q is not a volume name", volume)
		case strings.Contains(volume, "-bundle-"):
			return fmt.Errorf("%q is named like a bundle volume", volume)
		case used[volume]:
			return fmt.Errorf("volume %q is bound twice", volume)
		}
		used[volume] = true
		return nil
	}
	declared := map[string]bool{}
	for _, d := range s.Data {
		declared[d.Name] = true
		volume, ok := b.Volumes[d.Name]
		if !ok {
			return bad("data volume %q is not bound to a volume of the install", d.Name)
		}
		if err := use(volume); err != nil {
			return bad("data volume %q: %v", d.Name, err)
		}
		rev.Mounts = append(rev.Mounts, Mount{Name: volume, Target: d.Target})
	}
	extra := make([]string, 0, len(b.Volumes))
	for name := range b.Volumes {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return bad("%q is not a data volume of the Spec", extra[0])
	}
	switch {
	case len(r.Secrets) > 0 && b.Secrets == "":
		return bad("the runtime reads secrets, so it needs a secrets volume")
	case len(r.Secrets) == 0 && b.Secrets != "":
		return bad("the runtime has no secrets, so it takes no secrets volume")
	case b.Secrets != "":
		if err := use(b.Secrets); err != nil {
			return bad("the secrets volume: %v", err)
		}
		rev.Mounts = append(rev.Mounts, Mount{Name: b.Secrets, Target: SecretsDir, ReadOnly: true})
	}
	return rev, nil
}
