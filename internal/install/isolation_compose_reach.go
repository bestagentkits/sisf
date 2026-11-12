package install

import (
	"bytes"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The rules on what each service reaches beyond its networks and mounts:
// the network aliases of the edge's inner listeners, the release labels of
// the app container, and the Docker request filters, the only services with
// Docker access. CheckCompose applies them through composeChecker.reach.

const ruleReach = "reach.unexpected"

// networkList is a service's networks, written as a list or as a map whose
// values may name aliases. Either way it holds the network names; the
// aliases are read by networkAliases.
type networkList []string

func (n *networkList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := node.Decode(&names); err != nil {
			return err
		}
		*n = names
		return nil
	case yaml.MappingNode:
		attached, err := decodeAttachments(node)
		if err != nil {
			return err
		}
		*n = slices.Sorted(maps.Keys(attached))
		return nil
	}
	return invalidf("networks must be a list or a map")
}

// decodeAttachments reads the map form of a service's networks. The only
// setting a network may carry is its aliases; any other, such as a fixed
// address, refuses the file.
func decodeAttachments(node *yaml.Node) (map[string][]string, error) {
	var raw map[string]map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return nil, invalidf("a network attachment has a shape this deployment does not write")
	}
	out := map[string][]string{}
	for network, settings := range raw {
		out[network] = nil
		for key, value := range settings {
			if key != "aliases" {
				return nil, invalidf("the attachment to network %s sets %s; only aliases are written", network, key)
			}
			var aliases []string
			if err := value.Decode(&aliases); err != nil {
				return nil, invalidf("the aliases on network %s are not a list", network)
			}
			out[network] = aliases
		}
	}
	return out, nil
}

// networkAliases reads the aliases of every service, by network.
func networkAliases(data []byte) (map[string]map[string][]string, error) {
	var file struct {
		Services map[string]struct {
			Networks yaml.Node `yaml:"networks"`
		} `yaml:"services"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&file); err != nil {
		return nil, invalidf("the Compose file cannot be read: %v", err)
	}
	out := map[string]map[string][]string{}
	for name, s := range file.Services {
		if s.Networks.Kind != yaml.MappingNode {
			continue
		}
		attached, err := decodeAttachments(&s.Networks)
		if err != nil {
			return nil, err
		}
		for network, aliases := range attached {
			if len(aliases) > 0 {
				if out[name] == nil {
					out[name] = map[string][]string{}
				}
				out[name][network] = aliases
			}
		}
	}
	return out, nil
}

// reach applies the rules of this file to one service.
func (c *composeChecker) reach(name string, s composeService, rule serviceRule) {
	got := c.aliases[name]
	for _, network := range sortedKeys(got) {
		if !slices.Equal(got[network], rule.aliases[network]) {
			c.add(ruleReach, name, "has aliases %v on the %s network; it may have %v", got[network], network, rule.aliases[network])
		}
	}
	for _, network := range sortedKeys(rule.aliases) {
		if _, ok := got[network]; !ok {
			c.add(ruleReach, name, "lacks its alias %v on the %s network", rule.aliases[network], network)
		}
	}
	c.releaseLabels(name, s)
	c.filter(name, s, rule)
	if name == "edge" {
		c.edgeListeners(s)
	}
}

// releaseLabels allows labels on the app container alone: the three that
// name its release, matching its name and the revision it reports.
func (c *composeChecker) releaseLabels(name string, s composeService) {
	if name != "app" {
		if len(s.Labels) > 0 {
			c.add(ruleReach, name, "carries labels %v; only the app container is labelled, with its release", sortedKeys(s.Labels))
		}
		return
	}
	digest := core.Digest(s.Labels["sisf.digest"])
	want := map[string]string{"sisf.factory": c.file.Name, "sisf.unit": "app", "sisf.digest": string(digest)}
	if digest.Validate() != nil || !maps.Equal(s.Labels, want) {
		c.add(ruleReach, name, "labels %v are not exactly the factory, the unit app and the release's digest", s.Labels)
		return
	}
	if s.ContainerName != deployedContainer(core.FactoryID(c.file.Name), "app", digest) || s.Environment["SISF_REVISION"] != string(digest) {
		c.add(ruleReach, name, "its name or SISF_REVISION does not match the release it is labelled with")
	}
}

// filter keeps Docker access to the two filters, each serving its own role
// on its own network, with the socket's group and nothing else of the host.
func (c *composeChecker) filter(name string, s composeService, rule serviceRule) {
	if !rule.socket {
		if len(s.GroupAdd) > 0 {
			c.add(ruleSocket, name, "is added to groups %v; only the Docker request filters are, for the socket", s.GroupAdd)
		}
		return
	}
	if len(s.GroupAdd) != 1 || !gidPattern.MatchString(s.GroupAdd[0]) {
		c.add(ruleSocket, name, "must be added to the socket's group alone, by number, not %v", s.GroupAdd)
	}
	if !slices.Equal(s.Volumes, []string{socketMount}) {
		c.add(ruleSocket, name, "mounts %v; a filter mounts the Docker socket read-only and nothing else", s.Volumes)
	}
	role, network := "runner", c.file.Name+"-sandbox"
	if name == filterDeploy {
		role, network = "deploy", c.file.Name+"-app"
	}
	args := commandArgs(s.Command)
	if !slices.Contains(args, "--role="+role) || !slices.Contains(args, "--network="+network) {
		c.add(ruleSocket, name, "must serve the %s role on the %s network", role, network)
	}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--role=") && a != "--role="+role,
			strings.HasPrefix(a, "--network=") && a != "--network="+network,
			strings.HasPrefix(a, "--socket="),
			strings.HasPrefix(a, "--sim-image=") && role != "deploy":
			c.add(ruleSocket, name, "sets %s, which widens what the filter lets through", a)
		}
	}
}

// edgeListeners keeps the edge's admin listener and egress proxy each on
// the address of one network: neither may listen on every interface, and
// neither is published (ports).
func (c *composeChecker) edgeListeners(s composeService) {
	want := map[string]string{"--admin-listen": edgeAdminAlias + ":" + strconv.Itoa(PortEdgeAdmin), "--egress-listen": egressAlias + ":" + strconv.Itoa(PortEgress)}
	seen := map[string]bool{}
	for _, a := range commandArgs(s.Command) {
		flag, value, ok := strings.Cut(a, "=")
		if w, known := want[flag]; ok && known {
			seen[flag] = true
			if value != w {
				c.add(ruleReach, "edge", "sets %s=%s; it listens on %s, the address of its own network, only", flag, value, w)
			}
		}
	}
	for _, flag := range sortedKeys(want) {
		if !seen[flag] {
			c.add(ruleReach, "edge", "does not set %s, so the listener would take its default address", flag)
		}
	}
}

// commandArgs reads a service's command as a list of strings.
func commandArgs(node yaml.Node) []string {
	var args []string
	if node.Kind == yaml.SequenceNode {
		_ = node.Decode(&args) // a command of another shape has no arguments to check
	}
	sort.Strings(args)
	return args
}
