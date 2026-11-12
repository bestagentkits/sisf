package conformance

import (
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// zoneLabel renders a zone the way findings name it: "Zone 2 (agent)".
func zoneLabel(z core.Zone) string {
	if z.Number() == 0 {
		return fmt.Sprintf("the %s placement", z)
	}
	return fmt.Sprintf("Zone %d (%s)", z.Number(), z)
}

// checkZones requires a component for each of the three zones. A deployment
// missing one is not a factory deployment, however complete the rest is.
func (c *checker) checkZones() {
	for _, zone := range c.m.Components.Missing() {
		c.add(RuleZoneMissing, "components."+string(zone),
			fmt.Sprintf("declare components.%s with source, credentials_ref and store_role", zone),
			"%s has no component; all three zones must be declared in the one manifest of a factory deployment", zoneLabel(zone))
	}
}

// runsArtifact reports whether a unit runs something: every unit does,
// except a durable unit that names neither an image nor a command. Such a
// unit is plain storage (a SQLite file, a volume, a store kept elsewhere),
// so it has no artifact to pin and no process to place on a network.
func runsArtifact(name manifest.UnitName, unit manifest.Unit) bool {
	return name.Lifecycle() != manifest.LifecycleDurable || unit.Image != "" || len(unit.Command) > 0
}

// checkUnits requires all nine runtime units, each with the lifecycle its
// name always has and a pinned digest.
//
// The release manifest pins the digest of every unit, so every declared unit
// pins one, with two exceptions that have nothing left unpinned: a durable
// unit that names neither an image nor a command is plain storage and runs
// no artifact, and the supervisor unit may leave its digest out when the
// supervisor block pins it.
func (c *checker) checkUnits() {
	for _, name := range manifest.UnitNames() {
		unit, declared := c.m.Units[name]
		path := "units." + string(name)
		if !declared {
			c.add(RuleUnitMissing, path,
				fmt.Sprintf("declare units.%s with lifecycle: %s", name, name.Lifecycle()),
				"runtime unit %q of %s is not declared; one deployment has all nine units", name, zoneLabel(name.Zone()))
			continue
		}
		if unit.Lifecycle != name.Lifecycle() {
			c.add(RuleUnitLifecycle, path+".lifecycle",
				fmt.Sprintf("set units.%s.lifecycle to %s", name, name.Lifecycle()),
				"unit %q is declared %q, but a %s unit is always %s", name, unit.Lifecycle, name, name.Lifecycle())
		}
		pinnedBySupervisor := name == manifest.UnitSupervisor && c.m.Supervisor != nil
		if runsArtifact(name, unit) && !pinnedBySupervisor && unit.Digest.Validate() != nil {
			c.add(RuleUnitNotPinned, path+".digest",
				fmt.Sprintf("set units.%s.digest to the sha256 or sha512 digest of the unit's artifact", name),
				"unit %q does not pin a full content digest, so what runs as %q can change without the release manifest changing", name, name)
		}
	}
}

// checkNetworks requires the four network segments.
func (c *checker) checkNetworks() {
	for _, name := range manifest.NetworkNames() {
		if _, declared := c.m.Networks[name]; !declared {
			c.add(RuleNetworkMissing, "networks."+string(name),
				fmt.Sprintf("declare networks.%s", name),
				"network segment %q is not declared; the deployment is divided into the segments app, factory, control and sandbox", name)
		}
	}
}

// checkLimits requires the bounds that apply to every task whatever the
// budget mode, so that an unlimited budget never means an unbounded loop.
func (c *checker) checkLimits() {
	limits := c.m.Limits
	bounds := []struct {
		key     string
		bounded bool
	}{
		{"run_timeout", limits.RunTimeout > 0},
		{"max_attempts", limits.MaxAttempts >= 1},
		{"concurrency", limits.Concurrency >= 1},
		{"clarify_timeout", limits.ClarifyTimeout > 0},
	}
	for _, bound := range bounds {
		if !bound.bounded {
			c.add(RuleLimitsUnbounded, "limits."+bound.key,
				fmt.Sprintf("set limits.%s to a positive value", bound.key),
				"limits.%s is not positive, so a task would have no bound there; these limits apply even when the budget is unlimited", bound.key)
		}
	}
}
