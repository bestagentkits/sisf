package managedapp

import (
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Install is a managed application as one install runs it: the Spec the
// Owner installed and the revision derived from it under the install's
// volumes.
type Install struct {
	Spec     *Spec
	Revision RevisionConfig
}

// LoadInstall reads the Spec at specPath, an absolute path the Owner
// installed and never one inside a candidate's checkout, and derives its
// revision under b.
func LoadInstall(specPath string, b Binding) (*Install, error) {
	if !filepath.IsAbs(specPath) {
		return nil, core.NewError(core.CodeInvalidArgument, "the managed application's Spec must be given by an absolute path")
	}
	spec, err := Load(specPath)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	rev, err := spec.Revision(b)
	if err != nil {
		return nil, err
	}
	return &Install{Spec: spec, Revision: rev}, nil
}

// BindVolume reads one name=volume binding of a data volume, as a flag
// gives it. A name bound twice is refused.
func (b *Binding) BindVolume(v string) error {
	name, volume, ok := strings.Cut(v, "=")
	if !ok || name == "" || volume == "" {
		return core.NewError(core.CodeInvalidArgument, "a volume binding is name=volume")
	}
	if _, dup := b.Volumes[name]; dup {
		return core.NewError(core.CodeInvalidArgument, "data volume "+name+" is bound twice")
	}
	if b.Volumes == nil {
		b.Volumes = map[string]string{}
	}
	b.Volumes[name] = volume
	return nil
}
