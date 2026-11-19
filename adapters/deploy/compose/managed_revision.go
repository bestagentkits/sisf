package compose

import "github.com/bestagentkits/sisf/pkg/managedapp"

// UseRevision sets what every revision of a managed application is started
// with: its port, user, environment, bundle target and volumes. The deploy
// filter admits the same revision with dockerfilter.Config.AdmitRevision,
// so both read one derivation of the Spec and cannot disagree on an entry.
func (c *Config) UseRevision(r managedapp.RevisionConfig) {
	c.AppPort, c.User, c.Env, c.BundleTarget = r.Port, r.User, r.Env, r.BundleTarget
	c.Volumes = make([]VolumeMount, 0, len(r.Mounts))
	for _, m := range r.Mounts {
		c.Volumes = append(c.Volumes, VolumeMount{Name: m.Name, Target: m.Target, ReadOnly: m.ReadOnly})
	}
}
