package dockerfilter

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// forbiddenTmpfsRoots are where a tmpfs would hide or replace what the
// kernel or the runtime provides.
var forbiddenTmpfsRoots = []string{"/proc", "/sys", "/dev", "/etc", "/run"}

// checkHost judges the host configuration: the sandbox network and nothing
// of the host's, a read-only root file system, no privilege, every
// capability dropped, no-new-privileges, an allowed runtime and all three
// resource limits.
func (f *Filter) checkHost(h HostConfig) error {
	switch {
	case h.NetworkMode != f.cfg.SandboxNetwork:
		return fmt.Errorf("network mode %q is not the sandbox network", h.NetworkMode)
	case !h.ReadonlyRootfs:
		return errors.New("a runner needs a read-only root file system")
	case h.Privileged:
		return errors.New("privileged containers are refused")
	case len(h.CapAdd) > 0:
		return errors.New("adding capabilities is refused")
	case !slices.ContainsFunc(h.CapDrop, func(c string) bool { return strings.EqualFold(c, "ALL") }):
		return errors.New("a runner must drop every capability")
	case !f.runtimes[h.Runtime]:
		return fmt.Errorf("runtime %q is not allowed", h.Runtime)
	case h.Memory <= 0 || h.NanoCpus <= 0 || h.PidsLimit <= 0:
		return errors.New("a runner needs memory, CPU and process limits")
	case h.MemorySwap != h.Memory:
		return errors.New("a runner may not swap beyond its memory limit")
	}
	if err := checkSecurityOpt(h.SecurityOpt); err != nil {
		return err
	}
	if err := checkStrings("CapDrop", h.CapDrop, 64); err != nil {
		return err
	}
	return checkTmpfs(h.Tmpfs)
}

// checkSecurityOpt requires no-new-privileges and allows nothing else, so
// no seccomp, AppArmor, SELinux or label profile can be switched off.
func checkSecurityOpt(opts []string) error {
	if len(opts) == 0 {
		return errors.New("a runner needs no-new-privileges")
	}
	for _, opt := range opts {
		if !slices.Contains(noNewPrivileges, opt) {
			return fmt.Errorf("security option %q is not allowed", opt)
		}
	}
	return nil
}

// checkTmpfs allows scratch file systems in memory at clean absolute paths
// away from the kernel's and the runtime's, with plain options only.
func checkTmpfs(mounts map[string]string) error {
	if len(mounts) > maxTmpfs {
		return errors.New("too many tmpfs mounts")
	}
	for target, options := range mounts {
		if !path.IsAbs(target) || path.Clean(target) != target || target == "/" {
			return fmt.Errorf("tmpfs target %q must be a clean absolute path below /", target)
		}
		for _, root := range forbiddenTmpfsRoots {
			if target == root || strings.HasPrefix(target, root+"/") {
				return fmt.Errorf("tmpfs target %q is not allowed", target)
			}
		}
		if options == "" {
			continue
		}
		for _, opt := range strings.Split(options, ",") {
			if !tmpfsOption.MatchString(opt) {
				return fmt.Errorf("tmpfs option %q is not allowed", opt)
			}
		}
	}
	return nil
}
