package install

import (
	"bytes"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Violation is one broken isolation rule.
type Violation struct {
	// Rule names the rule; Service is the service it was found in, or "".
	Rule, Service, Message string
}

func (v Violation) String() string {
	if v.Service == "" {
		return "[" + v.Rule + "] " + v.Message
	}
	return "[" + v.Rule + "] " + v.Service + ": " + v.Message
}

// CheckCompose reads a rendered Compose file and returns every isolation
// rule it breaks. It is the static half of the zone isolation guarantee: the
// same checks run in the tests, before `sisf up` starts anything, and in the
// doctor, so the file that runs is the file that was checked.
func CheckCompose(l Layout, data []byte) ([]Violation, error) {
	var file composeFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Strictly: a key the deployment does not write, which could widen a
	// zone's reach in a way no rule below knows of, refuses the file.
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return nil, invalidf("the Compose file has a key this deployment does not write, or a value of another shape: %v", err)
	}
	aliases, err := networkAliases(data)
	if err != nil {
		return nil, err
	}
	c := &composeChecker{l: l, file: file, aliases: aliases}
	c.names()
	c.hostMaterial()
	for _, name := range sortedKeys(file.Services) {
		c.service(name, file.Services[name])
	}
	c.sandbox()
	c.stateReach()
	return c.out, nil
}

type composeChecker struct {
	l    Layout
	file composeFile
	// aliases are the network aliases of each service, by network.
	aliases map[string]map[string][]string
	out     []Violation
}

func (c *composeChecker) add(rule, service, format string, args ...any) {
	c.out = append(c.out, Violation{Rule: rule, Service: service, Message: fmt.Sprintf(format, args...)})
}

// names requires the project and everything it creates to carry the
// project's prefix, which itself starts with "sisf-".
func (c *composeChecker) names() {
	prefix := c.file.Name + "-"
	if !strings.HasPrefix(c.file.Name, "sisf-") {
		c.add(ruleNaming, "", "project name %q must start with sisf-", c.file.Name)
	}
	for _, n := range sortedKeys(c.file.Networks) {
		if !strings.HasPrefix(c.file.Networks[n].Name, prefix) {
			c.add(ruleNaming, "", "network %s must be named %s...", n, prefix)
		}
	}
	for _, n := range sortedKeys(c.file.Volumes) {
		if !strings.HasPrefix(c.file.Volumes[n].Name, prefix) {
			c.add(ruleNaming, "", "volume %s must be named %s...", n, prefix)
		}
	}
}

func (c *composeChecker) service(name string, s composeService) {
	c.dockerAccess(name, s)
	rule, known := serviceRules[name]
	if !known {
		c.add(ruleService, name, "is not a service this deployment has")
		return
	}
	if !digestPinned(s.Image) {
		c.add(ruleImagePinned, name, "image %q is not pinned by digest", s.Image)
	}
	if !strings.HasPrefix(s.ContainerName, c.file.Name+"-") {
		c.add(ruleNaming, name, "container_name %q must start with %s-", s.ContainerName, c.file.Name)
	}
	c.hardening(name, s)
	c.secrets(name, s, rule)
	c.networks(name, s, rule)
	c.ports(name, s, rule)
	c.mounts(name, s, rule)
	c.reach(name, s, rule)
}

// hardening keeps each service confined: unprivileged, without added
// capabilities or host devices, in namespaces of its own and under Docker's
// default seccomp, AppArmor and SELinux confinement.
func (c *composeChecker) hardening(name string, s composeService) {
	if s.Privileged {
		c.add(ruleHardening, name, "must not be privileged")
	}
	if len(s.CapAdd) > 0 {
		c.add(ruleHardening, name, "must not add capabilities %v", s.CapAdd)
	}
	if len(s.Devices) > 0 {
		c.add(ruleHardening, name, "must not be given host devices")
	}
	for _, ns := range []struct{ key, value string }{
		{"pid", s.Pid}, {"ipc", s.Ipc}, {"uts", s.Uts}, {"cgroup", s.Cgroup}, {"userns_mode", s.UsernsMode},
	} {
		if ns.value != "" {
			c.add(ruleHardening, name, "sets %s %q; it must keep a namespace of its own, not share the host's or another container's", ns.key, ns.value)
		}
	}
	for _, opt := range s.SecurityOpt {
		if opt != confinementOption {
			c.add(ruleHardening, name, "sets security_opt %q; only %s is allowed, any other option changes the confinement", opt, confinementOption)
		}
	}
	if !slices.Contains(s.SecurityOpt, confinementOption) {
		c.add(ruleHardening, name, "must set no-new-privileges")
	}
	if name != "state" && !slices.Contains(s.CapDrop, "ALL") {
		c.add(ruleHardening, name, "must drop all capabilities")
	}
}

// secrets keeps each service to the env files of its own area and secrets
// out of the Compose file itself.
func (c *composeChecker) secrets(name string, s composeService, rule serviceRule) {
	var want []string
	for _, f := range rule.envFiles {
		want = append(want, c.l.Dir+"/secrets/"+f)
	}
	got := slices.Clone(s.EnvFile)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		c.add(ruleSecretMount, name, "env files %v are not the ones of its own area, %v", got, want)
	}
	for _, key := range sortedKeys(s.Environment) {
		for _, word := range secretWords {
			if strings.Contains(key, word) {
				c.add(ruleSecretInline, name, "sets %s in the Compose file; secrets belong in the env file of the zone", key)
			}
		}
	}
}

func (c *composeChecker) networks(name string, s composeService, rule serviceRule) {
	if s.NetworkMode != "" && s.NetworkMode != rule.networkMode {
		c.add(ruleNetwork, name, "network_mode %q gives it a network outside its segments", s.NetworkMode)
	}
	if rule.networkMode != "" && len(s.Networks) > 0 {
		c.add(ruleNetwork, name, "is attached to networks %v besides the namespace it shares", []string(s.Networks))
	}
	for _, n := range s.Networks {
		if !slices.Contains(rule.networks, n) {
			c.add(ruleNetwork, name, "is attached to the %s network; it may use %v", n, rule.networks)
		}
	}
	if s.NetworkMode == "" && len(s.Networks) == 0 {
		c.add(ruleNetwork, name, "is attached to no declared network")
	}
}

// sandbox requires the sandbox to be internal and to hold the edge alone,
// and the docker network, where the filters serve, to be internal.
func (c *composeChecker) sandbox() {
	if sb, ok := c.file.Networks["sandbox"]; !ok || !sb.Internal {
		c.add(ruleNetwork, "", "the sandbox network must be internal, so runners have no route out except through the edge")
	}
	if d, ok := c.file.Networks[dockerNetwork]; !ok || !d.Internal {
		c.add(ruleNetwork, "", "the docker network must be internal: the Docker request filters serve on it and have no other way out")
	}
	for _, name := range sortedKeys(c.file.Services) {
		if slices.Contains(c.file.Services[name].Networks, "sandbox") && name != "edge" {
			c.add(ruleNetwork, name, "is on the sandbox network, which only the edge may join")
		}
	}
}

// ports allows only loopback publishing of the fixed port range.
func (c *composeChecker) ports(name string, s composeService, rule serviceRule) {
	for _, p := range s.Ports {
		parts := strings.Split(p, ":")
		if len(parts) != 3 || parts[0] != "127.0.0.1" {
			c.add(rulePort, name, "publishes %q; ports are bound to 127.0.0.1 as 127.0.0.1:host:container", p)
			continue
		}
		host, err := strconv.Atoi(parts[1])
		if err != nil || host < PortEdge || host > PortPostgres || !slices.Contains(rule.ports, host) {
			c.add(rulePort, name, "publishes host port %s, which is not one it owns in 7460-7469", parts[1])
		}
		if _, _, err := net.SplitHostPort(parts[0] + ":" + parts[1]); err != nil {
			c.add(rulePort, name, "publishes %q, which is not a valid binding", p)
		}
	}
}

// hostMaterial refuses what would put host files into a container past the
// mount rules: a named volume that is a bind of a host directory, or any
// other driver, and the top-level configs and secrets, which are host files.
func (c *composeChecker) hostMaterial() {
	for _, n := range sortedKeys(c.file.Volumes) {
		v := c.file.Volumes[n]
		if v.Driver != "" || len(v.DriverOpts) > 0 {
			c.add(ruleVolume, "", "volume %s sets a driver or driver options; a volume here is a plain named volume, never a bind of a host path", n)
		}
	}
	for _, n := range sortedKeys(c.file.Configs) {
		c.add(ruleSecretMount, "", "declares the config %s; host files reach no container except the manifests mounted read-only", n)
	}
	for _, n := range sortedKeys(c.file.Secrets) {
		c.add(ruleSecretMount, "", "declares the secret %s; each zone reads its secrets from the env file of its own area", n)
	}
}

// hostFileAllowed reports whether a service may mount the host path src:
// the manifest and the release manifest, the scripted agent's directory for
// the dispatcher, and the Docker socket for the filters (dockerAccess
// checks how).
func (c *composeChecker) hostFileAllowed(src string, rule serviceRule) bool {
	switch {
	case src == c.l.Config() || src == c.l.ReleaseManifest():
		return true
	case src == c.l.SimAgentDir():
		return rule.simAgent
	case src == dockerSocket:
		return rule.socket
	}
	return false
}

// mounts allows named volumes of the service's own, and a few read-only host
// files. Only the filters have a Docker socket (dockerAccess); the secrets
// directory belongs to nobody, and no service takes another's mounts or is
// handed configs or secrets.
func (c *composeChecker) mounts(name string, s composeService, rule serviceRule) {
	if len(s.VolumesFrom) > 0 {
		c.add(ruleVolume, name, "takes the mounts of %v; it may mount only its own volumes", s.VolumesFrom)
	}
	if len(s.Configs) > 0 || len(s.Secrets) > 0 {
		c.add(ruleSecretMount, name, "is handed configs or secrets; it reads its secrets from the env file of its own area")
	}
	for _, m := range s.Volumes {
		src, rest, _ := strings.Cut(m, ":")
		switch {
		case strings.HasPrefix(src, "/"):
			if strings.HasPrefix(src, c.l.Dir+"/secrets") {
				c.add(ruleSecretMount, name, "mounts the secrets directory")
			} else if !c.hostFileAllowed(src, rule) || !strings.HasSuffix(rest, ":ro") {
				c.add(ruleVolume, name, "mounts host path %s; only the manifest and the release manifest, read-only, are allowed", src)
			}
		default:
			if _, declared := c.file.Volumes[src]; !declared || !slices.Contains(rule.volumes, src) {
				c.add(ruleVolume, name, "mounts volume %q; it may mount %v", src, rule.volumes)
			}
		}
	}
}

// dockerAccess refuses every way a service could reach a Docker API: a
// mounted runtime socket, DOCKER_HOST, a socket proxy by name or image, and
// the privilege that makes a container the host. No unit of this release
// creates containers, so none may be able to.
func (c *composeChecker) dockerAccess(name string, s composeService) {
	filter := serviceRules[name].socket
	for _, m := range s.Volumes {
		for _, sock := range dockerSocketNames {
			if strings.Contains(m, sock) && (!filter || m != socketMount) {
				c.add(ruleSocket, name, "mounts %q; only the Docker request filters mount the Docker socket, read-only", m)
			}
		}
	}
	if _, set := s.Environment["DOCKER_HOST"]; set {
		c.add(ruleSocket, name, "must not set DOCKER_HOST; no service has a Docker API")
	}
	if s.Privileged {
		c.add(ruleSocket, name, "must not be privileged; a privileged container can reach the host and its Docker")
	}
	for _, word := range socketProxyWords {
		if strings.Contains(name, word) || strings.Contains(s.Image, word) || strings.Contains(s.ContainerName, word) {
			c.add(ruleProxy, name, "is a Docker socket proxy; there is none in this deployment")
			break
		}
	}
}

// stateReach keeps the state database off every network the edge is on: the
// edge faces the outside and must have no route to the store.
func (c *composeChecker) stateReach() {
	state, ok := c.file.Services["state"]
	edge, edgeOK := c.file.Services["edge"]
	if !ok || !edgeOK {
		return
	}
	for _, n := range state.Networks {
		if slices.Contains(edge.Networks, n) {
			c.add(ruleNetwork, "state", "shares the %s network with the edge, which must have no route to the state database", n)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
