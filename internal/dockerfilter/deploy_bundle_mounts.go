package dockerfilter

// Mounts of the deploy role. A managed application's revision runs a runtime
// image with a content bundle: a reproducible tar of its document root that
// the supervisor has verified. The bundle reaches a revision only through a
// named volume, never a host path:
//
//   - The bundle volume is named <factory>-bundle-<first 12 hex of the
//     bundle digest> and labelled sisf.factory and sisf.bundle.
//   - A loader fills it once: a container named <factory>-loader-<the same
//     12 hex>, labelled exactly sisf.factory, sisf.loader=1 and sisf.bundle,
//     from an allowed image, on no network, with no command, no environment
//     and the bundle volume as its only mount, read-write at
//     DeployLoaderTarget. A loader is never started; the bundle is uploaded
//     into it with the archive endpoint, at DeployLoaderTarget only, and the
//     loader is then removed.
//   - A revision mounts at most one bundle volume, read-only, at the
//     configured BundleTarget, and besides it only the configured data
//     volumes, each at its own target. Any other volume, a bind, a tmpfs or
//     any mount option is refused.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Labels and the fixed path of a bundle loader and a bundle volume; they
// match the deployment backend's.
const (
	// DeployLabelLoader marks a bundle loader; its value is always "1".
	DeployLabelLoader = "sisf.loader"
	// DeployLabelBundle names the bundle digest a loader or a bundle volume
	// holds.
	DeployLabelBundle = "sisf.bundle"
	// DeployLoaderTarget is where a loader mounts the bundle volume, and the
	// only path a bundle may be uploaded at.
	DeployLoaderTarget = "/sisf-bundle"
	// DefaultMaxArchiveBytes bounds an uploaded bundle: 1 GiB.
	DefaultMaxArchiveBytes = 1 << 30
	// maxDeployMounts bounds the mounts of one revision.
	maxDeployMounts = 16
)

// DeployVolume is a named data volume a revision may mount at Target. Its
// content outlives every revision.
type DeployVolume struct {
	Name   string
	Target string
}

// deployMount is the only mount the deploy role forwards: a named volume,
// read-only or not, with no options.
type deployMount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

var (
	deployMountKeys = []string{"Type", "Source", "Target", "ReadOnly"}
	// deployUser is a numeric uid:gid, neither of them 0.
	deployUser    = regexp.MustCompile(`^[1-9][0-9]{0,9}:[1-9][0-9]{0,9}$`)
	bundleHexPart = regexp.MustCompile(`^[a-f0-9]{12}$`)
)

// checkBundleSettings validates the bundle target, the data volumes and the
// environment of the deploy role.
func (c Config) checkBundleSettings() error {
	if c.BundleTarget != "" && (!cleanTarget(c.BundleTarget) || c.BundleTarget == DeployLoaderTarget) {
		return invalid("the bundle target must be a clean absolute path below / other than the loader's")
	}
	if c.MaxArchiveBytes < 0 {
		return invalid("the archive limit must be positive")
	}
	names, targets := map[string]bool{}, map[string]bool{c.BundleTarget: c.BundleTarget != ""}
	for _, v := range c.DataVolumes {
		switch {
		case !networkNamePattern.MatchString(v.Name) || !cleanTarget(v.Target):
			return invalid("a data volume needs a volume name and a clean absolute target")
		case strings.HasPrefix(v.Name, string(c.RunnerValue)+"-bundle-"):
			return invalid("data volume " + v.Name + " is named like a bundle volume")
		case names[v.Name] || targets[v.Target]:
			return invalid("data volume " + v.Name + " repeats a name or a target")
		}
		names[v.Name], targets[v.Target] = true, true
	}
	if err := checkEnv(c.DeployEnv); err != nil {
		return invalid(err.Error())
	}
	for _, kv := range c.DeployEnv {
		name, _, _ := strings.Cut(kv, "=")
		if name == DeployRevisionEnv || strings.HasPrefix(name, "DOCKER_") {
			return invalid("the deploy environment may not set the revision or a Docker variable")
		}
	}
	return nil
}

// cleanTarget is a clean absolute path below the root.
func cleanTarget(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && p != "/" && len(p) <= 4096 && !strings.ContainsFunc(p, isControl)
}

// checkMountKeys refuses a mount with any key but the four the deploy role
// knows, so no bind, volume or tmpfs option reaches the daemon.
func checkMountKeys(raw json.RawMessage) error {
	var mounts []json.RawMessage
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return errors.New("the mounts must be a list")
	}
	if len(mounts) > maxDeployMounts {
		return errors.New("a deployed container has too many mounts")
	}
	for _, m := range mounts {
		if _, err := objectKeys(m, "a mount", deployMountKeys...); err != nil {
			return err
		}
	}
	return nil
}

// bundleVolumeHex returns the 12 hex of a bundle volume name of this
// factory, or false when name is not one.
func (f *Filter) bundleVolumeHex(name string) (string, bool) {
	hex, ok := strings.CutPrefix(name, string(f.cfg.RunnerValue)+"-bundle-")
	return hex, ok && bundleHexPart.MatchString(hex)
}

// checkRevisionMounts allows at most one bundle volume, read-only at the
// bundle target, and the configured data volumes at their targets.
func (f *Filter) checkRevisionMounts(mounts []deployMount) error {
	targets := map[string]bool{}
	bundles := 0
	for _, m := range mounts {
		if m.Type != "volume" {
			return fmt.Errorf("mount type %q is not allowed; only named volumes are", m.Type)
		}
		if targets[m.Target] {
			return fmt.Errorf("two mounts share the target %q", m.Target)
		}
		targets[m.Target] = true
		if _, bundle := f.bundleVolumeHex(m.Source); bundle {
			bundles++
			switch {
			case f.cfg.BundleTarget == "":
				return errors.New("this filter allows no bundle volume")
			case bundles > 1:
				return errors.New("a deployed revision mounts at most one bundle volume")
			case !m.ReadOnly:
				return errors.New("a deployed revision mounts its bundle read-only")
			case m.Target != f.cfg.BundleTarget:
				return fmt.Errorf("a bundle is mounted at %q only", f.cfg.BundleTarget)
			}
			continue
		}
		if !slices.Contains(f.cfg.DataVolumes, DeployVolume{Name: m.Source, Target: m.Target}) {
			return fmt.Errorf("volume %q at %q is not a configured data volume", m.Source, m.Target)
		}
	}
	return nil
}

// checkLoaderRequest judges the create of a bundle loader: a container that
// only ever holds the bundle volume open, read-write, for an upload.
func (f *Filter) checkLoaderRequest(name string, req *deployCreateRequest) error {
	bundle := req.Labels[DeployLabelBundle]
	hex := deployDigestPattern.FindStringSubmatch(bundle)
	h := req.HostConfig
	switch {
	case f.cfg.BundleTarget == "":
		return errors.New("this filter allows no bundle loader")
	case len(req.Labels) != 3 || req.Labels[DeployLabelFactory] != string(f.cfg.RunnerValue) || req.Labels[DeployLabelLoader] != "1" || hex == nil:
		return errors.New("a bundle loader carries exactly this factory's sisf.factory, sisf.loader=1 and sisf.bundle labels")
	case name != string(f.cfg.RunnerValue)+"-loader-"+hex[1][:12]:
		return errors.New("the loader name must be the factory and the bundle it loads")
	case len(req.Env) > 0:
		return errors.New("a bundle loader has no environment")
	case req.User != "" && !deployUser.MatchString(req.User):
		return errors.New("a bundle loader runs as its image's user or a numeric uid:gid other than root")
	case !f.images[req.Image]:
		return fmt.Errorf("image %q is not an allowed unit image", req.Image)
	case h.NetworkMode != "none":
		return errors.New("a bundle loader joins no network")
	case h.RestartPolicy.Name != "" && h.RestartPolicy.Name != "no":
		return errors.New("a bundle loader is never restarted")
	case len(h.Mounts) != 1:
		return errors.New("a bundle loader mounts its bundle volume and nothing else")
	}
	m := h.Mounts[0]
	if m.Type != "volume" || m.Source != string(f.cfg.RunnerValue)+"-bundle-"+hex[1][:12] || m.Target != DeployLoaderTarget || m.ReadOnly {
		return fmt.Errorf("a bundle loader mounts the volume of its bundle read-write at %s", DeployLoaderTarget)
	}
	return f.checkDeployHost(h)
}

// deployAction says whether the deploy role may act on a container with
// these labels: a revision may be inspected, started, stopped, read and
// removed without its volumes; a loader that is not running may be inspected,
// removed and uploaded into, and is never started. A loader may be removed
// with its anonymous volumes, the ones its image declares: its only named
// volume is the bundle's, which the daemon never removes with a container.
func (f *Filter) deployAction(rt route, labels map[string]string, running bool) *denial {
	e := rt.endpoint
	switch {
	case f.loader(labels):
		switch {
		case e == endpointStart:
			return deny(http.StatusForbidden, "a bundle loader is never started")
		case e == endpointArchive && running:
			return deny(http.StatusForbidden, "a bundle is uploaded only into a loader that is not running")
		case e != endpointInspect && e != endpointRemove && e != endpointArchive:
			return deny(http.StatusForbidden, "a bundle loader is only inspected, uploaded into and removed")
		}
		return nil
	case f.deployed(labels):
		switch {
		case e == endpointArchive:
			return deny(http.StatusForbidden, "nothing is uploaded into a deployed revision")
		case e == endpointRemove && !isFalse(rt.query.Get("v")) && rt.query.Has("v"):
			return deny(http.StatusForbidden, "a deployed revision is removed without its volumes")
		}
		return nil
	}
	return deny(http.StatusForbidden, "the container is not a deployed unit or a bundle loader of this factory")
}

// loader reports whether labels mark a bundle loader of this factory.
func (f *Filter) loader(labels map[string]string) bool {
	_, runner := labels[DefaultRunnerLabel]
	return !runner && labels[DeployLabelLoader] == "1" && labels[DeployLabelFactory] == string(f.cfg.RunnerValue) &&
		deployDigestPattern.MatchString(labels[DeployLabelBundle])
}
