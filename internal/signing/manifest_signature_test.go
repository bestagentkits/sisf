package signing

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestManifestSignedByTheRootKeyVerifies(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	signed, err := SignManifest(context.Background(), signer, validManifest())
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	got, err := signed.Verify(context.Background(), newVerifier(t, root))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.SupervisorExecutable != executableDigest || got.Units[manifest.UnitSupervisor] != supervisorDigest {
		t.Errorf("verified manifest = %+v, want the signed content", got)
	}
	if signed.Signature.KeyID != KeyID(root) || signed.Signature.Algorithm != Algorithm {
		t.Errorf("signature names key %q and algorithm %q", signed.Signature.KeyID, signed.Signature.Algorithm)
	}
}

func TestManifestSignedByAnotherKeyIsRejected(t *testing.T) {
	t.Parallel()
	attacker, _ := newSigner(t)
	root, _ := newKey(t)
	signed, err := SignManifest(context.Background(), attacker, validManifest())
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	verifier := newVerifier(t, root)

	_, err = signed.Verify(context.Background(), verifier)
	wantCode(t, err, core.CodeSignatureInvalid)

	// Claiming the root's key id does not make the attacker's signature valid.
	signed.Signature.KeyID = KeyID(root)
	_, err = signed.Verify(context.Background(), verifier)
	wantCode(t, err, core.CodeSignatureInvalid)
}

func TestTamperedManifestIsRejected(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	verifier := newVerifier(t, root)
	tampers := map[string]func(*SignedManifest){
		"supervisor executable digest replaced": func(s *SignedManifest) { s.Manifest.SupervisorExecutable = otherDigest },
		"unit digest replaced":                  func(s *SignedManifest) { s.Manifest.Units[manifest.UnitApp] = otherDigest },
		"unit added":                            func(s *SignedManifest) { s.Manifest.Units[manifest.UnitControl] = otherDigest },
		"unit removed":                          func(s *SignedManifest) { delete(s.Manifest.Units, manifest.UnitApp) },
		"moved to another factory":              func(s *SignedManifest) { s.Manifest.FactoryID = "other-factory" },
		"issue time changed":                    func(s *SignedManifest) { s.Manifest.IssuedAt = s.Manifest.IssuedAt.Add(time.Second) },
		"signature bit flipped":                 func(s *SignedManifest) { s.Signature.Value[0] ^= 1 },
		"signature truncated":                   func(s *SignedManifest) { s.Signature.Value = s.Signature.Value[:32] },
	}
	for name, tamper := range tampers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			signed, err := SignManifest(context.Background(), signer, validManifest())
			if err != nil {
				t.Fatalf("SignManifest: %v", err)
			}
			tamper(&signed)
			_, err = signed.Verify(context.Background(), verifier)
			wantCode(t, err, core.CodeSignatureInvalid)
		})
	}
}

func TestUnsignedManifestIsRejected(t *testing.T) {
	t.Parallel()
	root, _ := newKey(t)
	verifier := newVerifier(t, root)

	_, err := SignedManifest{Manifest: validManifest()}.Verify(context.Background(), verifier)
	wantCode(t, err, core.CodeSignatureInvalid)

	// A signature block with a key and an algorithm but no value is unsigned too.
	blank := SignedManifest{Manifest: validManifest(), Signature: core.Signature{KeyID: KeyID(root), Algorithm: Algorithm}}
	_, err = blank.Verify(context.Background(), verifier)
	wantCode(t, err, core.CodeSignatureInvalid)
}

func TestManifestIsNotVerifiedWithoutAVerifier(t *testing.T) {
	t.Parallel()
	signer, _ := newSigner(t)
	signed, err := SignManifest(context.Background(), signer, validManifest())
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if _, err := signed.Verify(context.Background(), nil); err == nil {
		t.Fatal("a manifest must not verify when there is nothing to verify it with")
	}
}

func TestSignatureOfAnotherKindOfRecordDoesNotVerifyAManifest(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	// The root key signs a record with the manifest's exact fields, but of
	// another type: only the record type in the payload differs.
	payload := validManifest().SigningPayload()
	asApproval := bytes.Replace(payload, []byte("domain=21:sisf.release_manifest\n"), []byte("domain=13:sisf.approval\n"), 1)
	if bytes.Equal(asApproval, payload) {
		t.Fatal("the manifest payload does not name its record type as expected")
	}
	sig, err := signer.Sign(context.Background(), asApproval)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	relabelled := SignedManifest{Manifest: validManifest(), Signature: sig}
	_, err = relabelled.Verify(context.Background(), newVerifier(t, root))
	wantCode(t, err, core.CodeSignatureInvalid)
}

func TestInvalidManifestIsNeitherSignedNorVerified(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	breaks := map[string]func(*ReleaseManifest){
		"unknown version":              func(m *ReleaseManifest) { m.APIVersion = "sisf.release/v0" },
		"bad factory id":               func(m *ReleaseManifest) { m.FactoryID = "Not Valid" },
		"no issue time":                func(m *ReleaseManifest) { m.IssuedAt = time.Time{} },
		"supervisor not pinned":        func(m *ReleaseManifest) { delete(m.Units, manifest.UnitSupervisor) },
		"unknown unit":                 func(m *ReleaseManifest) { m.Units["sidecar"] = otherDigest },
		"unit pinned by tag":           func(m *ReleaseManifest) { m.Units[manifest.UnitApp] = "latest" },
		"no supervisor executable":     func(m *ReleaseManifest) { m.SupervisorExecutable = "" },
		"abbreviated executable hash":  func(m *ReleaseManifest) { m.SupervisorExecutable = "sha256:abcd" },
		"no units":                     func(m *ReleaseManifest) { m.Units = nil },
		"executable digest not sha256": func(m *ReleaseManifest) { m.SupervisorExecutable = "sha512:" + core.Digest(strings.Repeat("a", 128)) },
	}
	for name, breakIt := range breaks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := validManifest()
			breakIt(&m)
			_, err := SignManifest(context.Background(), signer, m)
			wantCode(t, err, core.CodeInvalidArgument)

			// Signed anyway by something that skipped the check, it still
			// does not verify.
			sig, err := signer.Sign(context.Background(), m.SigningPayload())
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			_, err = SignedManifest{Manifest: m, Signature: sig}.Verify(context.Background(), newVerifier(t, root))
			wantCode(t, err, core.CodeInvalidArgument)
		})
	}
}

func TestSigningPayloadKeepsItsEncoding(t *testing.T) {
	t.Parallel()
	// Changing these bytes invalidates every signature already issued.
	const want = "sisf-signed-record\n" +
		"domain=21:sisf.release_manifest\n" +
		"version=1:1\n" +
		"api_version=15:sisf.release/v1\n" +
		"factory_id=10:acme-hello\n" +
		"issued_at=20:2026-11-10T08:00:00Z\n" +
		"units=1:2\n" +
		"unit=3:app\n" +
		"unit_digest=71:sha256:1111111111111111111111111111111111111111111111111111111111111111\n" +
		"unit=10:supervisor\n" +
		"unit_digest=71:sha256:3333333333333333333333333333333333333333333333333333333333333333\n" +
		"supervisor_executable=71:sha256:9999999999999999999999999999999999999999999999999999999999999999\n"
	if got := string(validManifest().SigningPayload()); got != want {
		t.Errorf("payload =\n%s\nwant\n%s", got, want)
	}

	// The same instant written in another zone is the same manifest.
	local := validManifest()
	local.IssuedAt = local.IssuedAt.In(time.FixedZone("ICT", 7*3600))
	if got := string(local.SigningPayload()); got != want {
		t.Errorf("payload depends on the time zone:\n%s", got)
	}
}

func TestManifestFileRoundTripKeepsTheSignatureValid(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	signed, err := SignManifest(context.Background(), signer, validManifest())
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	path := filepath.Join(t.TempDir(), "release.json")
	if err := WriteSignedManifest(path, signed); err != nil {
		t.Fatalf("WriteSignedManifest: %v", err)
	}
	loaded, err := ReadSignedManifest(path)
	if err != nil {
		t.Fatalf("ReadSignedManifest: %v", err)
	}
	if _, err := loaded.Verify(context.Background(), newVerifier(t, root)); err != nil {
		t.Fatalf("Verify after round trip: %v", err)
	}
}

func TestManifestFileEditedOnDiskIsRejected(t *testing.T) {
	t.Parallel()
	signer, root := newSigner(t)
	signed, err := SignManifest(context.Background(), signer, validManifest())
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	path := filepath.Join(t.TempDir(), "release.json")
	if err := WriteSignedManifest(path, signed); err != nil {
		t.Fatalf("WriteSignedManifest: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := strings.Replace(string(data), string(executableDigest), string(otherDigest), 1)
	if edited == string(data) {
		t.Fatal("the fixture does not contain the digest to edit")
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := ReadSignedManifest(path)
	if err != nil {
		t.Fatalf("ReadSignedManifest: %v", err)
	}
	_, err = loaded.Verify(context.Background(), newVerifier(t, root))
	wantCode(t, err, core.CodeSignatureInvalid)
}

func TestManifestFileThatCannotBeTrustedAsWrittenIsRejected(t *testing.T) {
	t.Parallel()
	const body = `{"manifest":{"api_version":"sisf.release/v1"}}`
	cases := map[string]string{
		"field the signature does not cover": `{"manifest":{"api_version":"sisf.release/v1","note":"x"}}`,
		"unknown top-level field":            `{"manifest":{"api_version":"sisf.release/v1"},"override":{}}`,
		"second document after the first":    body + body,
		"not json":                           "units: {}",
		"empty file":                         "",
		"larger than a manifest can be":      `{"manifest":{"api_version":"` + strings.Repeat("a", maxManifestBytes) + `"}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "release.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := ReadSignedManifest(path)
			wantCode(t, err, core.CodeInvalidArgument)
		})
	}
}

func TestMissingManifestFileIsReportedAsMissing(t *testing.T) {
	t.Parallel()
	_, err := ReadSignedManifest(filepath.Join(t.TempDir(), "absent.json"))
	wantCode(t, err, core.CodeNotFound)
}
