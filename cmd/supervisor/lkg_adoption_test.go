package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// adoptionBackend records what it was asked to stage and promote, and
// observes whatever it last promoted.
type adoptionBackend struct {
	deploy.Backend
	staged   []deploy.Artifact
	promoted []deploy.Revision
	stageErr error
}

func (b *adoptionBackend) Stage(_ context.Context, a deploy.Artifact) (deploy.Revision, error) {
	if b.stageErr != nil {
		return deploy.Revision{}, b.stageErr
	}
	b.staged = append(b.staged, a)
	return deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: "blue"}, nil
}

func (b *adoptionBackend) Promote(_ context.Context, rev deploy.Revision, pct int) error {
	if pct != 100 {
		return core.NewError(core.CodeInvalidArgument, "only whole switches")
	}
	b.promoted = append(b.promoted, rev)
	return nil
}

func (b *adoptionBackend) ObservedRevision(_ context.Context, unit string) (deploy.Observed, error) {
	if len(b.promoted) == 0 {
		return deploy.Observed{Unit: unit}, nil
	}
	last := b.promoted[len(b.promoted)-1]
	return deploy.Observed{Unit: unit, Digest: last.Digest, Healthy: true}, nil
}

// installedBook is an LKG book whose only record is the release the root
// key signed at install, pinning app at digest.
func installedBook(t *testing.T, digest core.Digest) (*release.LKGBook, core.Signer) {
	t.Helper()
	return installedBookIn(t, filepath.Join(t.TempDir(), "trust"), digest)
}

// installedBookIn is installedBook on a trust volume made at dir.
func installedBookIn(t *testing.T, dir string, digest core.Digest) (*release.LKGBook, core.Signer) {
	t.Helper()
	ctx := context.Background()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := signing.NewVerifier(pub)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signing.SignManifest(ctx, root, signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion, FactoryID: testFactory, IssuedAt: time.Now().UTC(),
		Units:                map[manifest.UnitName]core.Digest{manifest.UnitApp: digest, manifest.UnitSupervisor: digest},
		SupervisorExecutable: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := supervisor.OpenLKGStore(dir, filepath.Join(dir, supervisor.DefaultLKGFile), testFactory, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, supervisor.LKGRecord{Release: signed, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	book, err := release.OpenLKGBook(dir, testFactory, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return book, root
}

// Under the local deploy backend the supervisor starts the installed LKG
// itself, signed as a deploy artifact, and sends it all traffic; once it is
// observed serving, nothing more is staged.
func TestTheSupervisorStartsTheInstalledLastKnownGoodRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	digest := core.Digest("sha256:" + strings.Repeat("b", 64))
	book, signer := installedBook(t, digest)
	backend := &adoptionBackend{}
	a := &lkgAdopter{factory: testFactory, backend: backend, lkg: book, signer: signer, image: "local/" + string(testFactory) + "/app"}

	if err := a.adopt(ctx); err != nil {
		t.Fatal(err)
	}
	if len(backend.staged) != 1 || backend.staged[0].Digest != core.ReleaseDigest(digest) || backend.staged[0].Unit != "app" ||
		backend.staged[0].Image != a.image+"@"+string(digest) || backend.staged[0].Signature.Validate() != nil {
		t.Fatalf("staged %+v", backend.staged)
	}
	if len(backend.promoted) != 1 || backend.promoted[0].Digest != core.ReleaseDigest(digest) {
		t.Fatalf("promoted %+v", backend.promoted)
	}
	if err := a.adopt(ctx); err != nil || len(backend.staged) != 1 {
		t.Fatalf("an LKG already serving was staged again: %v, %d stages", err, len(backend.staged))
	}
	backend.stageErr = core.NewError(core.CodeUnavailable, "the port is taken")
	backend.promoted = nil
	if err := a.adopt(ctx); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a failed start was not reported: %v", err)
	}
}

// Only the trusted simulation judges on a short window or a low sample
// floor, and a simulated unit image must be pinned.
func TestReleaseTuningIsShorterOnlyInTheSimulation(t *testing.T) {
	t.Parallel()
	short := releaseTuning{window: 10 * time.Second, minSamples: 20}
	if m, err := short.monitor(true); err != nil || m.Window != 10*time.Second || m.MinSamples != 20 {
		t.Fatalf("the simulation: %+v, %v", m, err)
	}
	if _, err := short.monitor(false); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a production factory took a short window: %v", err)
	}
	long := releaseTuning{window: release.DefaultWindow, minSamples: release.DefaultMinSamples}
	if _, err := long.monitor(false); err != nil {
		t.Fatalf("the defaults: %v", err)
	}
}
