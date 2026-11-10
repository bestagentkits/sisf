package conformance

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// allowedSegments lists, for every unit, the network segments it may attach
// to. The application stays on its own segment; Zone 2 reaches Zone 3 over
// the factory segment, where the control unit serves its API; the supervisor
// is reachable from the control segment alone; the runners sit in the
// sandbox; the two durable units serve each zone on that zone's segment and
// are never reachable from the sandbox; the edge fronts the application and
// the control unit and is the sandbox's way out.
var allowedSegments = map[manifest.UnitName][]manifest.NetworkName{
	manifest.UnitApp:        {manifest.NetworkApp},
	manifest.UnitControl:    {manifest.NetworkControl, manifest.NetworkFactory},
	manifest.UnitSupervisor: {manifest.NetworkControl},
	manifest.UnitDispatcher: {manifest.NetworkFactory},
	manifest.UnitRunner:     {manifest.NetworkSandbox},
	manifest.UnitQARunner:   {manifest.NetworkSandbox},
	manifest.UnitState:      {manifest.NetworkApp, manifest.NetworkFactory, manifest.NetworkControl},
	manifest.UnitArtifacts:  {manifest.NetworkApp, manifest.NetworkFactory, manifest.NetworkControl},
	manifest.UnitEdge:       {manifest.NetworkApp, manifest.NetworkControl, manifest.NetworkSandbox},
}

// checkSegments keeps every declared unit on the segments its role allows,
// and requires a unit that runs to be attached to at least one of them: a
// unit attached to none lands on whatever network the platform gives by
// default, outside the four segments.
func (c *checker) checkSegments() {
	for _, name := range manifest.UnitNames() {
		unit, declared := c.m.Units[name]
		if !declared {
			continue
		}
		allowed := allowedSegments[name]
		path := fmt.Sprintf("units.%s.networks", name)
		attach := fmt.Sprintf("attach units.%s to %s only", name, segmentList(allowed))
		if len(unit.Networks) == 0 && runsArtifact(name, unit) {
			c.add(RuleNetworkUnattached, path, attach,
				"unit %q of %s is attached to no network segment, so it would run on a network this manifest does not divide", name, zoneLabel(name.Zone()))
		}
		for _, attached := range unit.Networks {
			if !slices.Contains(allowed, attached) {
				c.add(RuleNetworkSegment, path, attach,
					"unit %q of %s is attached to the %q segment; it may attach to %s only, so it has no path into a segment its role does not need", name, zoneLabel(name.Zone()), attached, segmentList(allowed))
			}
		}
	}
}

// segmentList renders segments as "control, factory".
func segmentList(segments []manifest.NetworkName) string {
	names := make([]string, len(segments))
	for i, segment := range segments {
		names[i] = string(segment)
	}
	return strings.Join(names, ", ")
}

// checkEgress requires the sandbox segment to have no route of its own to
// the outside and to name its way out, and requires every named way out to
// be a declared unit that sits on the segment and is not itself a runner.
func (c *checker) checkEgress() {
	for _, segment := range manifest.NetworkNames() {
		network, declared := c.m.Networks[segment]
		if !declared {
			continue
		}
		path := fmt.Sprintf("networks.%s.egress_via", segment)
		if segment == manifest.NetworkSandbox {
			if !network.Internal {
				c.add(RuleSandboxNotInternal, "networks.sandbox.internal",
					"set networks.sandbox.internal to true",
					"the sandbox segment is not internal, so a runner has a route to the outside that bypasses the egress proxy and its policy")
			}
			if network.EgressVia == "" {
				c.add(RuleEgressProxy, path,
					"set networks.sandbox.egress_via to the unit that enforces the egress policy, such as edge",
					"the sandbox segment names no egress proxy, so nothing enforces the egress policy on what the runners send out")
				continue
			}
		}
		if network.EgressVia == "" {
			continue
		}
		proxy := manifest.UnitName(network.EgressVia)
		unit, exists := c.m.Units[proxy]
		fix := fmt.Sprintf("set networks.%s.egress_via to a declared unit attached to the %s segment", segment, segment)
		switch {
		case !proxy.Valid() || !exists:
			c.add(RuleEgressProxy, path, fix,
				"the %s segment sends its outbound traffic through %q, which is not a declared runtime unit, so nothing stands where the policy is enforced", segment, network.EgressVia)
		case proxy == manifest.UnitRunner || proxy == manifest.UnitQARunner:
			c.add(RuleEgressProxy, path, fix+" that is not a runner",
				"the %s segment sends its outbound traffic through %q; a runner executes agent code and cannot be what polices it", segment, proxy)
		case !slices.Contains(unit.Networks, segment):
			c.add(RuleEgressProxy, path, fix+fmt.Sprintf(", or attach units.%s to it", proxy),
				"the %s segment sends its outbound traffic through unit %q, which is not attached to that segment and so cannot be reached from it", segment, proxy)
		}
	}
}

// checkPublicExposure lets only the units that serve the outside be exposed:
// the application and the edge, and the control unit for the routes it
// names. The control plane as a whole is never public.
func (c *checker) checkPublicExposure() {
	for _, name := range manifest.UnitNames() {
		unit, declared := c.m.Units[name]
		if !declared || !unit.Public {
			continue
		}
		path := "units." + string(name)
		switch name {
		case manifest.UnitApp, manifest.UnitEdge:
		case manifest.UnitControl:
			if len(unit.PublicRoutes) == 0 {
				c.add(RulePublicExposure, path+".public_routes",
					"list the routes to expose in units.control.public_routes, such as the webhook route and the report endpoint",
					"the control unit is public without naming routes, which exposes the whole control plane; only the routes it names may be reached from outside")
			}
			for i, route := range unit.PublicRoutes {
				if !strings.HasPrefix(route, "/") || strings.Trim(route, "/") == "" {
					c.add(RulePublicExposure, fmt.Sprintf("%s.public_routes[%d]", path, i),
						"name one route below the root, such as /webhooks/github",
						"public route %q of the control unit is not a path below the root, so it exposes the whole control plane or nothing that can be told", route)
				}
			}
		default:
			c.add(RulePublicExposure, path+".public",
				fmt.Sprintf("remove public from units.%s; only app, edge and named routes of control face the outside", name),
				"unit %q of %s is exposed outside the deployment; only the application, the edge and named routes of the control unit may be", name, zoneLabel(name.Zone()))
		}
	}
}
