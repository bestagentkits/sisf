package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestSupervisorStartsWhenItsDigestMatchesTheSignedManifest(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	status := d.mustStart().Status()

	if !status.SelfCheck.Passed || status.SelfCheck.ExecutableDigest != runningDigest {
		t.Errorf("self check = %+v, want it passed for %s", status.SelfCheck, runningDigest)
	}
	if status.SelfCheck.ManifestKeyID != signing.KeyID(d.root) {
		t.Errorf("manifest key = %q, want the root key %q", status.SelfCheck.ManifestKeyID, signing.KeyID(d.root))
	}
	if status.FactoryID != testFactory || !status.StartedAt.Equal(d.clock.now) {
		t.Errorf("status = %+v, want factory %s started at %s", status, testFactory, d.clock.now)
	}
}

func TestSupervisorRefusesToStartWhenItsDigestDiffersFromTheSignedManifest(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	socket := filepath.Join(shortTempDir(t), "admin.sock")
	d.cfg.AdminAddr = "unix:" + socket
	d.measured = otherDigest // a supervisor other than the one the Owner signed for

	sup, err := d.start()
	refused(t, sup, err, core.CodeTampered)
	if !strings.Contains(err.Error(), string(otherDigest)) || !strings.Contains(err.Error(), string(runningDigest)) {
		t.Errorf("the refusal must name both digests: %v", err)
	}
	if _, statErr := os.Lstat(socket); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a refused supervisor must not open its admin socket (stat: %v)", statErr)
	}
}

func TestSupervisorRefusesToStartWhenTheManifestIsSignedByAnotherKey(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	attacker, _ := newManifestSigner(t)
	// The attacker pins a supervisor of its own and signs with its own key.
	forged := d.release()
	forged.SupervisorExecutable = otherDigest
	d.writeManifest(forged, attacker)
	d.measured = otherDigest

	sup, err := d.start()
	refused(t, sup, err, core.CodeSignatureInvalid)
}

func TestSupervisorRefusesToStartWhenTheManifestWasEdited(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	data, err := os.ReadFile(d.cfg.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	// The pinned digest is rewritten to match a replaced supervisor; the
	// Owner's signature is left in place.
	edited := strings.Replace(string(data), string(runningDigest), string(otherDigest), 1)
	if edited == string(data) {
		t.Fatal("the fixture does not contain the digest to edit")
	}
	if err := os.WriteFile(d.cfg.ManifestPath, []byte(edited), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	d.measured = otherDigest

	sup, err := d.start()
	refused(t, sup, err, core.CodeSignatureInvalid)
}

func TestSupervisorRefusesToStartOnAnUnsignedManifest(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	unsigned := signing.SignedManifest{Manifest: d.release()}
	if err := signing.WriteSignedManifest(d.cfg.ManifestPath, unsigned); err != nil {
		t.Fatalf("WriteSignedManifest: %v", err)
	}

	sup, err := d.start()
	refused(t, sup, err, core.CodeSignatureInvalid)
}

func TestSupervisorRefusesToStartWithoutAManifest(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	if err := os.Remove(d.cfg.ManifestPath); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	sup, err := d.start()
	refused(t, sup, err, core.CodeNotFound)

	d.cfg.ManifestPath = ""
	sup, err = d.start()
	refused(t, sup, err, core.CodeInvalidArgument)
}

func TestSupervisorRefusesToStartWithoutTheRootKey(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		change func(*deployment)
		code   core.ErrorCode
	}{
		"variable not set":     {func(d *deployment) { delete(d.env, rootKeyVariable) }, core.CodeNotFound},
		"variable blank":       {func(d *deployment) { d.env[rootKeyVariable] = "" }, core.CodeNotFound},
		"no reference":         {func(d *deployment) { d.cfg.RootKeyRef = "" }, core.CodeNotFound},
		"value is not a key":   {func(d *deployment) { d.env[rootKeyVariable] = "not a key" }, core.CodeInvalidArgument},
		"unknown key location": {func(d *deployment) { d.cfg.RootKeyRef = "vault:root" }, core.CodeInvalidArgument},
		"store not readable":   {func(d *deployment) { d.cfg.RootKeyRef = "local:keys/root.pub" }, core.CodeNotSupported},
		"key given inline": {func(d *deployment) {
			d.cfg.RootKeyRef = signing.EncodePublicKey(d.root)
		}, core.CodeInvalidArgument},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			tc.change(d)
			sup, err := d.start()
			refused(t, sup, err, tc.code)
		})
	}
}

func TestSupervisorRefusesToStartWhenTheRootKeyIsNotTheOneThatSigned(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	_, other := newManifestSigner(t)
	d.env[rootKeyVariable] = signing.EncodePublicKey(other)

	sup, err := d.start()
	refused(t, sup, err, core.CodeSignatureInvalid)
}

func TestSupervisorRefusesAManifestSignedForAnotherFactory(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	// Correctly signed by the same root key, but for another deployment.
	foreign := d.release()
	foreign.FactoryID = "other-factory"
	d.writeManifest(foreign, d.signer)

	sup, err := d.start()
	refused(t, sup, err, core.CodePermissionDenied)

	d.cfg.FactoryID = "Not A Factory"
	sup, err = d.start()
	refused(t, sup, err, core.CodeInvalidArgument)
}

func TestSupervisorRefusesToStartWhenItCannotMeasureItself(t *testing.T) {
	t.Parallel()
	failures := map[string]Measure{
		"measurement fails":           func() (core.Digest, error) { return "", errors.New("executable is gone") },
		"measurement is not a digest": func() (core.Digest, error) { return "latest", nil },
		"measurement is empty":        func() (core.Digest, error) { return "", nil },
	}
	for name, measure := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			options := d.options()
			options.Measure = measure
			sup, err := Start(t.Context(), d.cfg, options)
			if sup != nil || err == nil {
				t.Fatalf("Start = %v, %v; want a refusal", sup, err)
			}
		})
	}
}

func TestExecutableDigestIsTheSha256OfTheFileThatIsRunning(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	const sha256OfHello core.Digest = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	got, err := DigestFile(path)
	if err != nil || got != sha256OfHello {
		t.Fatalf("DigestFile = %q, %v; want %q", got, err, sha256OfHello)
	}
	if _, err := DigestFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a file that does not exist has no digest")
	}

	running, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	want, err := DigestFile(running)
	if err != nil {
		t.Fatalf("DigestFile(%s): %v", running, err)
	}
	measured, err := MeasureExecutable()
	if err != nil || measured != want {
		t.Fatalf("MeasureExecutable = %q, %v; want the digest of %s, %q", measured, err, running, want)
	}
}
