package dockerfilter

import (
	"slices"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// AdmitRevision lets the deploy role admit the revisions of a managed
// application exactly as the Compose backend starts them with
// compose.Config.UseRevision: the runtime image, the bundle target, the
// environment and every volume with its read-only flag.
func (c *Config) AdmitRevision(r managedapp.RevisionConfig) {
	if !slices.Contains(c.Images, r.Image) {
		c.Images = append(c.Images, r.Image)
	}
	c.BundleTarget, c.DeployEnv = r.BundleTarget, r.Env
	c.DataVolumes = make([]DeployVolume, 0, len(r.Mounts))
	for _, m := range r.Mounts {
		c.DataVolumes = append(c.DataVolumes, DeployVolume{Name: m.Name, Target: m.Target, ReadOnly: m.ReadOnly})
	}
}
