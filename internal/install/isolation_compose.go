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
	dec.KnownFields(false)
	if err := dec.Decode(&file); err != nil {
		return nil, invalidf("the Compose file cannot be read: %v", err)
	}
	c := &composeChecker{l: l, file: file}
	c.names()
	for _, name := range sortedKeys(file.Services) {
		c.service(name, file.Services[name])
	}
	c.sandbox()
	return c.out, nil
}

type composeChecker struct {
	l    Layout
	file composeFile
	out  []Violation
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
	if name == "socket-proxy" {
		c.proxy(s)
	}
	if _, holds := s.Environment["DOCKER_HOST"]; holds && name != "dispatcher" {
		c.add(ruleSocket, name, "must not be pointed at the Docker API")
	}
	if name == "dispatcher" && s.Environment["DOCKER_HOST"] != "tcp://socket-proxy:2375" {
		c.add(ruleSocket, name, "must reach Docker only through tcp://socket-proxy:2375")
	}
}

func (c *composeChecker) hardening(name string, s composeService) {
	if s.Privileged || s.Pid != "" || s.Ipc != "" || len(s.CapAdd) > 0 {
		c.add(ruleHardening, name, "must not be privileged, share a host namespace or add capabilities")
	}
	if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
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
		if name == "socket-proxy" {
			break // its variables are API group switches, checked by proxy()
		}
		for _, word := range secretWords {
			if strings.Contains(key, word) {
				c.add(ruleSecretInline, name, "sets %s in the Compose file; secrets belong in the env file of the zone", key)
			}
		}
	}
}

func (c *composeChecker) networks(name string, s composeService, rule serviceRule) {
	if s.NetworkMode != "" && (name != "sim-github" || s.NetworkMode != "service:control") {
		c.add(ruleNetwork, name, "network_mode %q gives it a network outside the four segments", s.NetworkMode)
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

// sandbox requires the sandbox to be internal and to hold the edge alone.
func (c *composeChecker) sandbox() {
	if sb, ok := c.file.Networks["sandbox"]; !ok || !sb.Internal {
		c.add(ruleNetwork, "", "the sandbox network must be internal, so runners have no route out except through the edge")
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

// mounts allows named volumes of the service's own, and a few read-only host
// files. The Docker socket belongs to the socket proxy; the secrets
// directory belongs to nobody.
func (c *composeChecker) mounts(name string, s composeService, rule serviceRule) {
	for _, m := range s.Volumes {
		src, rest, _ := strings.Cut(m, ":")
		switch {
		case strings.HasSuffix(src, "docker.sock"):
			if name != "socket-proxy" || !strings.HasSuffix(m, ":ro") {
				c.add(ruleSocket, name, "mounts the Docker socket; only the socket proxy may, read-only")
			}
		case strings.HasPrefix(src, "/"):
			if strings.HasPrefix(src, c.l.Dir+"/secrets") {
				c.add(ruleSecretMount, name, "mounts the secrets directory")
			} else if (src != c.l.Config() && src != c.l.ReleaseManifest()) || !strings.HasSuffix(rest, ":ro") {
				c.add(ruleVolume, name, "mounts host path %s; only the manifest and the release manifest, read-only, are allowed", src)
			}
		default:
			if _, declared := c.file.Volumes[src]; !declared || !slices.Contains(rule.volumes, src) {
				c.add(ruleVolume, name, "mounts volume %q; it may mount %v", src, rule.volumes)
			}
		}
	}
}

// proxy holds the socket proxy to the container API alone.
func (c *composeChecker) proxy(s composeService) {
	for key, value := range s.Environment {
		on := value == "1"
		if on && !slices.Contains(proxyAllowed, key) {
			c.add(ruleProxy, "socket-proxy", "enables %s; only %v may be on", key, proxyAllowed)
		}
	}
	for _, key := range proxyAllowed[:2] {
		if s.Environment[key] != "1" {
			c.add(ruleProxy, "socket-proxy", "must enable %s, or the dispatcher cannot create and destroy runners", key)
		}
	}
	for _, key := range proxyDenied {
		if s.Environment[key] != "0" {
			c.add(ruleProxy, "socket-proxy", "must switch %s off explicitly", key)
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
