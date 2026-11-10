package conformance

import (
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkIdentity requires that the factory, this installation of it and the
// runtime units can be told apart by identifier alone. Telemetry, labels and
// paths carry these identifiers side by side; if two of them are equal, a
// record no longer says which factory, which install or which unit it means.
func (c *checker) checkIdentity() {
	factory, deployment := c.m.FactoryID, c.m.DeploymentID
	factoryOK, deploymentOK := true, true
	if err := factory.Validate(); err != nil {
		factoryOK = false
		c.add(RuleIdentityInvalid, "factory_id",
			"use 1-63 characters of [a-z0-9._-] starting with a letter or digit",
			"factory_id %q cannot identify the deployment in labels, paths and logs", string(factory))
	}
	if err := deployment.Validate(); err != nil {
		deploymentOK = false
		c.add(RuleIdentityInvalid, "deployment_id",
			"use 1-63 characters of [a-z0-9._-] starting with a letter or digit",
			"deployment_id %q cannot identify this installation in labels, paths and logs", string(deployment))
	}
	if factoryOK && deploymentOK && string(factory) == string(deployment) {
		c.add(RuleIdentityNotDistinct, "deployment_id",
			"give this installation a name of its own, such as \"local\" or \"staging\"",
			"deployment_id equals factory_id %q, so a record cannot tell which factory from which installation of it", string(factory))
	}
	// A runtime unit is addressed by its fixed name; an identity that reuses
	// one would make "app" mean both a factory and a unit of every factory.
	if factoryOK && manifest.UnitName(factory).Valid() {
		c.add(RuleIdentityNotDistinct, "factory_id",
			"choose a factory_id that is not the name of a runtime unit",
			"factory_id %q is the name of a runtime unit, so the factory cannot be told from one of its units", string(factory))
	}
	if deploymentOK && manifest.UnitName(deployment).Valid() {
		c.add(RuleIdentityNotDistinct, "deployment_id",
			"choose a deployment_id that is not the name of a runtime unit",
			"deployment_id %q is the name of a runtime unit, so the installation cannot be told from one of its units", string(deployment))
	}
}
