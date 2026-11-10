package supervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Values used by the fixtures. The digests only need to be well formed.
const (
	testFactory      core.FactoryID = "acme-hello"
	rootKeyVariable                 = "SISF_ROOT_PUBLIC_KEY"
	appDigest        core.Digest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	supervisorDigest core.Digest    = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	runningDigest    core.Digest    = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	otherDigest      core.Digest    = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// fixedClock is a core.Clock that stands still, so no test waits on time.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func (c fixedClock) After(time.Duration) <-chan time.Time {
	fired := make(chan time.Time, 1)
	fired <- c.now
	return fired
}

// deployment is one installed supervisor as a test sees it: a root key
// generated for the test, a signed release manifest on disk, a trust
// directory only the owner can write, and the digest the supervisor will
// measure of itself. A test changes one of these and starts the supervisor.
type deployment struct {
	t        *testing.T
	dir      string
	root     ed25519.PublicKey
	signer   *signing.Signer
	cfg      Config
	env      map[string]string
	measured core.Digest
	clock    fixedClock
}

// newDeployment returns a deployment in which every startup check passes.
func newDeployment(t *testing.T) *deployment {
	t.Helper()
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust")
	if err := os.Mkdir(trust, 0o700); err != nil {
		t.Fatalf("create trust directory: %v", err)
	}
	signer, root := newManifestSigner(t)
	d := &deployment{
		t:      t,
		dir:    dir,
		root:   root,
		signer: signer,
		cfg: Config{
			FactoryID:    testFactory,
			ManifestPath: filepath.Join(dir, "release.json"),
			RootKeyRef:   "env:" + rootKeyVariable,
			TrustDir:     trust,
			AdminAddr:    "127.0.0.1:0",
		},
		env:      map[string]string{rootKeyVariable: signing.EncodePublicKey(root)},
		measured: runningDigest,
		clock:    fixedClock{now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
	}
	d.writeManifest(d.release(), signer)
	return d
}

// newManifestSigner returns a release-manifest signer with a fresh key.
func newManifestSigner(t *testing.T) (*signing.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub
}

// release is the manifest that pins the digest the supervisor measures.
func (d *deployment) release() signing.ReleaseManifest {
	return signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion,
		FactoryID:  testFactory,
		IssuedAt:   time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		Units: map[manifest.UnitName]core.Digest{
			manifest.UnitApp:        appDigest,
			manifest.UnitSupervisor: supervisorDigest,
		},
		SupervisorExecutable: runningDigest,
	}
}

// sign returns m signed by signer.
func (d *deployment) sign(m signing.ReleaseManifest, signer core.Signer) signing.SignedManifest {
	d.t.Helper()
	signed, err := signing.SignManifest(context.Background(), signer, m)
	if err != nil {
		d.t.Fatalf("SignManifest: %v", err)
	}
	return signed
}

// writeManifest replaces the manifest file with m signed by signer.
func (d *deployment) writeManifest(m signing.ReleaseManifest, signer core.Signer) {
	d.t.Helper()
	if err := signing.WriteSignedManifest(d.cfg.ManifestPath, d.sign(m, signer)); err != nil {
		d.t.Fatalf("WriteSignedManifest: %v", err)
	}
}

// options are the supervisor's dependencies, all in-process.
func (d *deployment) options() Options {
	return Options{
		Clock: d.clock,
		LookupEnv: func(name string) (string, bool) {
			value, ok := d.env[name]
			return value, ok
		},
		Measure: func() (core.Digest, error) { return d.measured, nil },
	}
}

// start runs the startup checks. A started supervisor is closed with the test.
func (d *deployment) start() (*Supervisor, error) {
	d.t.Helper()
	sup, err := Start(context.Background(), d.cfg, d.options())
	if err == nil {
		d.t.Cleanup(func() { _ = sup.Close() })
	}
	return sup, err
}

// mustStart fails the test when the supervisor refuses to start.
func (d *deployment) mustStart() *Supervisor {
	d.t.Helper()
	sup, err := d.start()
	if err != nil {
		d.t.Fatalf("Start: %v", err)
	}
	return sup
}

// lkgStore opens the deployment's LKG store the way the supervisor does.
func (d *deployment) lkgStore() *LKGStore {
	d.t.Helper()
	verifier, err := signing.NewVerifier(d.root)
	if err != nil {
		d.t.Fatalf("NewVerifier: %v", err)
	}
	store, err := OpenLKGStore(d.cfg.TrustDir, filepath.Join(d.cfg.TrustDir, DefaultLKGFile), testFactory, verifier)
	if err != nil {
		d.t.Fatalf("OpenLKGStore: %v", err)
	}
	return store
}

// refused fails the test unless the supervisor refused to start with code.
func refused(t *testing.T, sup *Supervisor, err error, code core.ErrorCode) {
	t.Helper()
	if sup != nil {
		t.Fatal("a supervisor was returned although it must refuse to start")
	}
	if !core.HasCode(err, code) {
		t.Fatalf("Start error = %v, want code %s", err, code)
	}
}

// shortTempDir returns a directory whose path is short enough to hold a Unix
// socket; the per-test directory of the testing package can exceed the limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sv")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// encodeRecord renders an LKG record the way the store writes it.
func encodeRecord(t *testing.T, record LKGRecord) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return data
}
