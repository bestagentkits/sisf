package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Trust is what the Owner set up for the supervisor: the root key, the
// supervisor's delegated keys, and the trust volume with the root-signed
// release that pins each unit's last known good build.
type Trust struct {
	Verifier *delegation.ChainVerifier
	QA       *delegation.DelegatedSigner
	Artifact *delegation.DelegatedSigner
	LKG      *release.LKGBook
	// Supervisor is the digest the release manifest pins for the supervisor.
	Supervisor core.Digest
	// Installed are the units' first releases.
	Installed map[string]core.ReleaseDigest
	// Dir is the trust volume the LKG book is kept on.
	Dir string
}

// digestOf names a fixed build of the harness.
func digestOf(name string) core.Digest {
	sum := sha256.Sum256([]byte("harness build " + name))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// newTrust issues the keys and records the installed release.
func newTrust(t testingT, factory core.FactoryID, clock core.Clock) *Trust {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	root, err := signing.NewSigner(priv)
	must(t, err)
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: factory, Root: pub, Clock: clock})
	must(t, err)
	load := func(purpose delegation.Purpose) *delegation.DelegatedSigner {
		issued, err := delegation.Issue(ctx, root, delegation.IssueRequest{
			FactoryID: factory, Purpose: purpose, Holder: delegation.HolderSupervisor,
			NotBefore: clock.Now(), Lifetime: 7 * 24 * time.Hour,
		}, nil)
		must(t, err)
		signer, err := delegation.Load(ctx, verifier, delegation.LoadRequest{
			Holder: delegation.HolderSupervisor, Delegation: issued.Delegation, PrivateKey: issued.PrivateKey,
		})
		must(t, err)
		return signer
	}
	tr := &Trust{Verifier: verifier, QA: load(delegation.PurposeQAReceipt), Artifact: load(delegation.PurposeDeployArtifact),
		Supervisor: digestOf(string(factory) + " supervisor"),
		Installed: map[string]core.ReleaseDigest{
			"app":     core.ReleaseDigest(digestOf(string(factory) + " app v1")),
			"control": core.ReleaseDigest(digestOf(string(factory) + " control v1")),
		}}

	dir := filepath.Join(t.TempDir(), "trust")
	must(t, os.Mkdir(dir, 0o700))
	tr.Dir = dir
	rootVerifier, err := signing.NewVerifier(pub)
	must(t, err)
	store, err := supervisor.OpenLKGStore(dir, filepath.Join(dir, supervisor.DefaultLKGFile), factory, rootVerifier)
	must(t, err)
	units := map[manifest.UnitName]core.Digest{manifest.UnitSupervisor: tr.Supervisor}
	for unit, digest := range tr.Installed {
		units[manifest.UnitName(unit)] = core.Digest(digest)
	}
	signed, err := signing.SignManifest(ctx, root, signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion, FactoryID: factory, IssuedAt: clock.Now(), Units: units, SupervisorExecutable: tr.Supervisor,
	})
	must(t, err)
	must(t, store.Save(ctx, supervisor.LKGRecord{Release: signed, RecordedAt: clock.Now()}))
	tr.LKG, err = release.OpenLKGBook(dir, factory, store, clock)
	must(t, err)
	return tr
}

// must fails the test on err.
func must(t testingT, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
