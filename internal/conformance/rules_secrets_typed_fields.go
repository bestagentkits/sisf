package conformance

import (
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkTypedTextSecrets searches the typed fields that hold free text for a
// value shaped like a secret: what each unit runs and its image, where each
// zone's source and health check are, where artifacts and the state store
// are kept, the supervisor's image, the synthetic suite and the hosts egress
// allows. Such a field names a place or
// a program; a credential pasted into it, as in a URL with a token, would be
// read by everyone who reads the manifest. The words of a command are also
// held to the flag and assignment forms a secret takes there.
func (c *checker) checkTypedTextSecrets() {
	for _, name := range manifest.UnitNames() {
		unit, declared := c.m.Units[name]
		if !declared {
			continue
		}
		at := "units." + string(name)
		c.refuseSecretShape(at+".image", unit.Image)
		for i, word := range unit.Command {
			place := fmt.Sprintf("%s.command[%d]", at, i)
			switch {
			case i > 0 && secretGivenToFlag(unit.Command[i-1], word):
				c.add(RuleSecretInline, place, "store the secret with a secret provider and pass "+manifest.SecretRefForm+" to the flag, or a file that holds it",
					"%s follows a flag named like a secret, so it is that secret written in the manifest (the value is not shown)", place)
			case assignsSecret(word):
				c.add(RuleSecretInline, place, "store the secret with a secret provider and assign "+manifest.SecretRefForm+" to the variable",
					"%s assigns a value to an environment variable named like a secret; a secret is never written in the manifest (the value is not shown)", place)
			default:
				c.refuseSecretShape(place, word)
			}
		}
		for i, route := range unit.PublicRoutes {
			c.refuseSecretShape(fmt.Sprintf("%s.public_routes[%d]", at, i), route)
		}
	}
	for _, zone := range core.Zones() {
		if component := c.m.Components.Zone(zone); component != nil {
			c.refuseSecretShape(fmt.Sprintf("components.%s.source", zone), component.Source)
			c.refuseSecretShape(fmt.Sprintf("components.%s.health_check", zone), component.HealthCheck)
		}
	}
	c.refuseSecretShape("storage.artifacts.location", c.m.Storage.Artifacts.Location)
	c.refuseSecretShape("storage.state.path", c.m.Storage.State.Path)
	c.refuseSecretShape("release.low_traffic.synthetic_suite", c.m.Release.LowTraffic.SyntheticSuite)
	for i, host := range c.m.Egress.Allow {
		c.refuseSecretShape(fmt.Sprintf("egress.allow[%d]", i), host)
	}
	if c.m.Supervisor != nil {
		c.refuseSecretShape("supervisor.image", c.m.Supervisor.Image)
	}
}

// refuseSecretShape reports a typed text field whose value has the shape of
// a secret.
func (c *checker) refuseSecretShape(at, value string) {
	if shape := secretShape(value); shape != "" {
		c.add(RuleSecretInline, at,
			"remove the credential from "+at+"; keep it with a secret provider and give the unit "+manifest.SecretRefForm+" through its credential set or environment",
			"%s holds %s; a secret is never written in the manifest, whatever the field is for (the value is not shown)", at, shape)
	}
}
