package conformance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkStoreRoles rejects a store role used by two zones. Role names are
// compared without regard to letter case, because a store that folds names
// (as PostgreSQL does with unquoted ones) opens both spellings as one role.
func (c *checker) checkStoreRoles() {
	zones := core.Zones()
	for i, later := range zones {
		component := c.m.Components.Zone(later)
		if component == nil || strings.TrimSpace(component.StoreRole) == "" {
			continue
		}
		for _, earlier := range zones[:i] {
			other := c.m.Components.Zone(earlier)
			if other == nil || !strings.EqualFold(strings.TrimSpace(component.StoreRole), strings.TrimSpace(other.StoreRole)) {
				continue
			}
			c.add(RuleSharedStoreRole, fmt.Sprintf("components.%s.store_role", later),
				"give each zone a state-store role of its own, different by more than letter case",
				"%s and %s open the state store with the same role %q, so one zone can read and write the other's data", zoneLabel(earlier), zoneLabel(later), component.StoreRole)
		}
	}
}

// secretHolder is one place in the manifest that names a secret.
type secretHolder struct {
	path, ref string
	// zone is set when the secret is a credential set, and says whose.
	zone core.Zone
	// component marks the credential set of a whole zone.
	component bool
	// label names the holder in a finding.
	label string
}

// secret says which secret the holder's reference names.
func (h secretHolder) secret() string {
	if h.zone != "" {
		return "the credential set of " + h.label
	}
	return h.label
}

// secretHolders lists every reference to a secret the manifest holds: the
// credential set of each zone, the credential set a unit names for itself,
// the state connection secret, the root public key, and each reference in
// an adapter's config, held by the zone that runs the adapter. The coding,
// QA and execution adapters run in Zone 2; the deployment adapter is run by
// the supervisor in Zone 3.
func (c *checker) secretHolders() []secretHolder {
	var holders []secretHolder
	for _, zone := range core.Zones() {
		if component := c.m.Components.Zone(zone); component != nil {
			holders = append(holders, secretHolder{
				path: fmt.Sprintf("components.%s.credentials_ref", zone), ref: component.CredentialsRef,
				zone: zone, component: true, label: zoneLabel(zone),
			})
		}
	}
	for _, name := range manifest.UnitNames() {
		if unit, declared := c.m.Units[name]; declared {
			holders = append(holders, secretHolder{
				path: fmt.Sprintf("units.%s.credentials_ref", name), ref: unit.CredentialsRef,
				zone: name.Zone(), label: fmt.Sprintf("unit %q of %s", name, zoneLabel(name.Zone())),
			})
		}
	}
	holders = append(holders, secretHolder{path: "storage.state.dsn_ref", ref: c.m.Storage.State.DSNRef, label: "the state connection secret"})
	if c.m.Supervisor != nil {
		holders = append(holders, secretHolder{path: "supervisor.root_key_ref", ref: c.m.Supervisor.RootKeyRef, label: "the root public key"})
	}
	for _, adapter := range []struct {
		name   string
		config map[string]any
		zone   core.Zone
	}{
		{"coder", c.m.Adapters.Coder.Config, core.ZoneAgent},
		{"qa", c.m.Adapters.QA.Config, core.ZoneAgent},
		{"execution", c.m.Adapters.Execution.Config, core.ZoneAgent},
		{"deploy", c.m.Adapters.Deploy.Config, core.ZoneControl},
	} {
		at := "adapters." + adapter.name + ".config"
		label := fmt.Sprintf("the %s adapter of %s", adapter.name, zoneLabel(adapter.zone))
		for _, found := range configReferences(at, adapter.config) {
			holders = append(holders, secretHolder{path: found.path, ref: found.ref, zone: adapter.zone, label: label})
		}
	}
	return holders
}

// configReference is one reference to a secret found in free-form config.
type configReference struct{ path, ref string }

// configReferences lists, in key order, the references a free-form config
// holds: text that is a reference, and the value of an assignment such as
// TOKEN=env:NAME.
func configReferences(path string, value any) []configReference {
	var found []configReference
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			found = append(found, configReferences(path+"."+key, v[key])...)
		}
	case map[any]any:
		named := map[string]any{}
		for key, inner := range v {
			if text, ok := key.(string); ok {
				named[text] = inner
			}
		}
		found = configReferences(path, named)
	case []any:
		for i, item := range v {
			found = append(found, configReferences(fmt.Sprintf("%s[%d]", path, i), item)...)
		}
	case string:
		if manifest.IsSecretRef(v) {
			found = append(found, configReference{path, v})
		} else if _, assigned, ok := strings.Cut(v, "="); ok && manifest.IsSecretRef(assigned) {
			found = append(found, configReference{path, assigned})
		}
	}
	return found
}

// checkSecretReferences rejects one reference standing for two secrets that
// must differ:
//
//   - two zones, or units of two zones, never use one credential set;
//   - a credential set is never the state connection secret or the root
//     public key, and those two are never one secret either.
//
// Units and adapters of one zone may share that zone's credential set;
// whether the qa-runner has one of its own is the QA rule's question. A finding names the
// two places and never the reference, which may be a secret pasted where a
// reference belongs.
func (c *checker) checkSecretReferences() {
	holders := c.secretHolders()
	for i, later := range holders {
		if later.ref == "" {
			continue
		}
		for _, earlier := range holders[:i] {
			if !strings.EqualFold(later.ref, earlier.ref) {
				continue
			}
			switch {
			case later.zone != "" && earlier.zone != "":
				if later.zone == earlier.zone {
					continue
				}
				fix := "give each zone a credential set of its own"
				if !later.component || !earlier.component {
					fix = fmt.Sprintf("reference a credential set at %s that no other zone uses", later.path)
				}
				c.add(RuleSharedCredentials, later.path, fix,
					"%s and %s reference the same credential set, so a breach of one zone reaches the other", earlier.label, later.label)
			default:
				c.add(RuleSecretRefReused, later.path,
					fmt.Sprintf("reference at %s a secret of its own, different from the one at %s", later.path, earlier.path),
					"%s (%s) and %s (%s) are one reference; each names a different secret with a different holder, so whoever may read one would read the other", earlier.secret(), earlier.path, later.secret(), later.path)
			}
		}
	}
}
