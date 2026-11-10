package signing

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// ReleaseAPIVersion is the release manifest version these types describe.
const ReleaseAPIVersion = "sisf.release/v1"

// Domain and field-list version of the release manifest's signing payload.
const (
	releasePayloadDomain  = "sisf.release_manifest"
	releasePayloadVersion = 1
)

// ReleaseManifest pins what one factory deployment runs. It is signed with
// the Owner's root key, and the digests in factory.yaml are the ones it pins.
// The signature covers SigningPayload, which holds every field.
type ReleaseManifest struct {
	APIVersion string `json:"api_version"`
	// FactoryID is the one deployment this manifest is for. A manifest signed
	// for another factory must not be accepted, even under the same root key.
	FactoryID core.FactoryID `json:"factory_id"`
	IssuedAt  time.Time      `json:"issued_at"`
	// Units pins the artifact of each unit that has one: an image digest in
	// Docker mode, a binary digest in Standalone. The supervisor is always
	// pinned.
	Units map[manifest.UnitName]core.Digest `json:"units"`
	// SupervisorExecutable is the SHA-256 of the supervisor's executable
	// file. It is the one digest a running supervisor can measure of itself
	// in every mode, and what its startup self-check compares against.
	SupervisorExecutable core.Digest `json:"supervisor_executable"`
}

// Validate rejects a manifest that is incomplete or pins anything by a name
// that can move. It does not verify a signature.
func (m ReleaseManifest) Validate() error {
	if m.APIVersion != ReleaseAPIVersion {
		return invalid("release manifest api_version %q is not %q", m.APIVersion, ReleaseAPIVersion)
	}
	if err := m.FactoryID.Validate(); err != nil {
		return err
	}
	if m.IssuedAt.IsZero() {
		return invalid("release manifest must say when it was issued")
	}
	// Units are visited in name order so the same manifest always reports
	// the same problem.
	for _, name := range slices.Sorted(maps.Keys(m.Units)) {
		if !name.Valid() {
			return invalid("release manifest pins unknown unit %q", string(name))
		}
		if err := m.Units[name].Validate(); err != nil {
			return invalid("release manifest unit %q is not pinned by a full digest", string(name)).WithCause(err)
		}
	}
	if _, pinned := m.Units[manifest.UnitSupervisor]; !pinned {
		return invalid("release manifest does not pin the supervisor unit")
	}
	if err := m.SupervisorExecutable.Validate(); err != nil || !strings.HasPrefix(string(m.SupervisorExecutable), "sha256:") {
		return invalid("release manifest supervisor_executable must be the sha256 digest of the supervisor's executable")
	}
	return nil
}

// SigningPayload returns the canonical bytes the root key signs and the
// supervisor verifies. Every field is bound in a fixed order, with the units
// counted and sorted by name, so no unit can be added, dropped or repinned
// and the manifest cannot be moved to another factory without invalidating
// its signature.
func (m ReleaseManifest) SigningPayload() []byte {
	b := core.NewPayloadBuilder(releasePayloadDomain, releasePayloadVersion).
		Text("api_version", m.APIVersion).
		Text("factory_id", string(m.FactoryID)).
		Time("issued_at", m.IssuedAt).
		Int("units", int64(len(m.Units)))
	for _, name := range slices.Sorted(maps.Keys(m.Units)) {
		b.Text("unit", string(name)).Text("unit_digest", string(m.Units[name]))
	}
	return b.Text("supervisor_executable", string(m.SupervisorExecutable)).Bytes()
}
