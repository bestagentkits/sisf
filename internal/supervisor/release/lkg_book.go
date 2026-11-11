package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Sources of a last known good release.
const (
	// SourceReleaseManifest is the release the Owner's root key signed at
	// install: the LKG until a release is promoted.
	SourceReleaseManifest = "release_manifest"
	// SourcePromoted is a release this supervisor verified and promoted.
	SourcePromoted = "promoted"
)

// maxLKGEntryBytes bounds how much of an LKG entry is read.
const maxLKGEntryBytes = 64 << 10

var unitPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// LKG is the last known good release of one unit: the revision a failed
// release is restored to.
//
// The rule that keeps an LKG restorable after the key that signed it
// expired or was revoked is: an artifact is verified when it is promoted,
// and only then. The supervisor records that fact, with the key that signed
// the artifact and the instant it verified it, on its trust volume, which no
// other zone and no candidate can write. A rollback trusts that record and
// does not verify the old signature again, because a delegated key stops
// verifying within seven days by design while the way back must stay open.
// No other path writes an entry, so an artifact that never passed
// verification can never become the LKG.
type LKG struct {
	Unit   string             `json:"unit"`
	Digest core.ReleaseDigest `json:"digest"`
	Image  string             `json:"image,omitempty"`
	Source string             `json:"source"`
	// IssueID and RunID name the release that was promoted.
	IssueID core.IssueRef `json:"issue_id,omitzero"`
	RunID   core.RunID    `json:"run_id,omitempty"`
	// ArtifactKeyID is the key whose signature was verified at promotion.
	ArtifactKeyID string    `json:"artifact_key_id,omitempty"`
	VerifiedAt    time.Time `json:"verified_at,omitzero"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// LKGBook keeps the LKG of each unit on the trust volume. Before any release
// is promoted, the LKG of a unit is the one pinned by the root-signed
// release manifest record the supervisor already keeps there.
type LKGBook struct {
	trustDir string
	factory  core.FactoryID
	manifest *supervisor.LKGStore
	clock    core.Clock
}

// OpenLKGBook returns the book kept in trustDir. manifest is the store of
// the root-signed release manifest record and may be nil when the install
// recorded none. A trust volume other accounts can write is refused.
func OpenLKGBook(trustDir string, factory core.FactoryID, manifest *supervisor.LKGStore, clock core.Clock) (*LKGBook, error) {
	if err := factory.Validate(); err != nil {
		return nil, err
	}
	if _, err := supervisor.TrustPath(trustDir, "lkg-probe.json"); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &LKGBook{trustDir: trustDir, factory: factory, manifest: manifest, clock: clock}, nil
}

// path returns the location of a unit's promoted entry.
func (b *LKGBook) path(unit string) (string, error) {
	if !unitPattern.MatchString(unit) {
		return "", invalid("unit %q is not a unit name", unit)
	}
	return supervisor.TrustPath(b.trustDir, "lkg-unit-"+unit+".json")
}

// Get returns the LKG of unit, or false when there is none. An entry that
// cannot be decoded, or names another unit, is TAMPERED: only the
// supervisor writes it.
func (b *LKGBook) Get(ctx context.Context, unit string) (LKG, bool, error) {
	p, err := b.path(unit)
	if err != nil {
		return LKG{}, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return b.fromManifest(ctx, unit)
	}
	if err != nil {
		return LKG{}, false, core.NewError(core.CodeUnavailable, "the LKG of "+unit+" cannot be read").WithCause(err)
	}
	var entry LKG
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(data), maxLKGEntryBytes))
	decoder.DisallowUnknownFields()
	if len(data) > maxLKGEntryBytes || decoder.Decode(&entry) != nil ||
		entry.Unit != unit || entry.Source != SourcePromoted || entry.Digest.Validate() != nil || entry.VerifiedAt.IsZero() {
		return LKG{}, false, core.NewError(core.CodeTampered, "the LKG entry of "+unit+" is not one the supervisor wrote")
	}
	return entry, true, nil
}

// Installed returns the digest the root-signed release manifest pins for
// unit: the release the install itself built, whatever was promoted since.
// It is false when the install recorded no release or pins no such unit.
func (b *LKGBook) Installed(ctx context.Context, unit string) (core.ReleaseDigest, bool, error) {
	if !unitPattern.MatchString(unit) {
		return "", false, invalid("unit %q is not a unit name", unit)
	}
	lkg, found, err := b.fromManifest(ctx, unit)
	return lkg.Digest, found, err
}

// fromManifest reads the unit's digest from the root-signed release
// manifest record. That store verifies the root signature as it loads.
func (b *LKGBook) fromManifest(ctx context.Context, unit string) (LKG, bool, error) {
	if b.manifest == nil {
		return LKG{}, false, nil
	}
	record, found, err := b.manifest.Load(ctx)
	if err != nil || !found {
		return LKG{}, false, err
	}
	digest, pinned := record.Release.Manifest.Units[manifest.UnitName(unit)]
	if !pinned {
		return LKG{}, false, nil
	}
	return LKG{Unit: unit, Digest: core.ReleaseDigest(digest), Source: SourceReleaseManifest, RecordedAt: record.RecordedAt}, true, nil
}

// record makes a verified, promoted release the LKG of its unit. It takes
// a verifiedRelease, which only the pipeline's verification can build.
func (b *LKGBook) record(v verifiedRelease) error {
	if v.verifiedAt.IsZero() || v.artifact.Signature.Validate() != nil || v.artifact.FactoryID != b.factory {
		return core.NewError(core.CodeSignatureInvalid, "only an artifact verified for this factory can become the LKG")
	}
	p, err := b.path(v.artifact.Unit)
	if err != nil {
		return err
	}
	entry := LKG{
		Unit: v.artifact.Unit, Digest: v.artifact.Digest, Image: v.artifact.Image, Source: SourcePromoted,
		IssueID: v.issue, RunID: v.run, ArtifactKeyID: v.artifact.Signature.KeyID,
		VerifiedAt: v.verifiedAt, RecordedAt: b.clock.Now().UTC(),
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return invalid("the LKG entry cannot be encoded").WithCause(err)
	}
	return supervisor.WriteTrustFile(p, append(data, '\n'))
}
