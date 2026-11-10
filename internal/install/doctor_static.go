package install

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkManifest runs the conformance rules over factory.yaml.
func (in *Installer) checkManifest(c *collector, m *manifest.Manifest) {
	report := conformance.Check(m)
	if err := report.Err(); err != nil {
		c.fail("manifest", "factory.yaml breaks the deployment rules: %v", err)
		return
	}
	c.ok("manifest", "factory %s in %s mode passes the deployment rules", m.FactoryID, m.Mode)
	var unbuiltUnits []string
	for _, name := range slices.Sorted(maps.Keys(m.Units)) {
		// The state unit has no artifact in Standalone mode: it is a file.
		if isUnbuilt(m.Units[name].Digest) && name != manifest.UnitState {
			unbuiltUnits = append(unbuiltUnits, string(name))
		}
	}
	if len(unbuiltUnits) > 0 {
		c.warn("artifacts", "not built yet, with placeholder digests: %s; a release must build them before tasks can run", strings.Join(unbuiltUnits, ", "))
	}
}

// checkRelease verifies the signed release manifest against the install's
// root public key and against the digests in factory.yaml.
func (in *Installer) checkRelease(ctx context.Context, c *collector, m *manifest.Manifest) {
	signed, err := signing.ReadSignedManifest(in.Layout.ReleaseManifest())
	if err != nil {
		c.fail("release manifest", "no usable signed release manifest: run `sisf up` to build and sign one (%v)", err)
		return
	}
	pubText, err := os.ReadFile(in.Layout.Secret(areaControl, "root-public-key"))
	if err != nil {
		c.fail("release manifest", "the root public key cannot be read, so the signature cannot be checked")
		return
	}
	pub, err := signing.ParsePublicKey(strings.TrimSpace(string(pubText)))
	if err != nil {
		c.fail("release manifest", "the root public key is unreadable: %v", err)
		return
	}
	verifier, err := signing.NewVerifier(pub)
	if err != nil {
		c.fail("release manifest", "%v", err)
		return
	}
	rm, err := signed.Verify(ctx, verifier)
	if err != nil {
		c.fail("release manifest", "the signature does not verify under this install's root key: %v", err)
		return
	}
	if rm.FactoryID != m.FactoryID {
		c.fail("release manifest", "it is signed for factory %s, not %s", rm.FactoryID, m.FactoryID)
		return
	}
	for name, digest := range rm.Units {
		if got := m.Units[name].Digest; got != digest {
			c.fail("release manifest", "unit %s is pinned to %s in factory.yaml but %s was signed", name, got, digest)
			return
		}
	}
	if m.Mode == manifest.ModeStandalone {
		got, err := supervisor.DigestFile(in.Layout.Bin("sisf-supervisor"))
		if err != nil || got != rm.SupervisorExecutable {
			c.fail("release manifest", "the supervisor program on disk is not the one that was signed: run `sisf up` to rebuild it")
			return
		}
	}
	c.ok("release manifest", "signed by key %s for %d units, matching factory.yaml", signing.KeyID(pub), len(rm.Units))
}

// checkSecrets runs the on-disk half of the zone isolation rules.
func (in *Installer) checkSecrets(c *collector, m *manifest.Manifest) {
	violations, err := CheckSecrets(in.Layout, m.Mode)
	if err != nil {
		c.fail("secrets", "the secrets directory cannot be inspected: %v", err)
		return
	}
	if len(violations) > 0 {
		for _, v := range violations {
			c.fail("secrets", "%s", v)
		}
		return
	}
	c.ok("secrets", "owner-only permissions, one credential per zone, nothing of Zone 3 outside it")
}

// checkCompose runs the isolation rules over the rendered Compose project.
func (in *Installer) checkCompose(c *collector) {
	data, err := os.ReadFile(in.Layout.Compose())
	if err != nil {
		c.fail("compose isolation", "the Compose file is missing: run `sisf up` to render it")
		return
	}
	violations, err := CheckCompose(in.Layout, data)
	if err != nil {
		c.fail("compose isolation", "the Compose file cannot be read: %v", err)
		return
	}
	if len(violations) > 0 {
		for _, v := range violations {
			c.fail("compose isolation", "%s", v)
		}
		return
	}
	c.ok("compose isolation", "pinned images, loopback ports, the Docker socket for the two filters only, one credential set per zone")
}
