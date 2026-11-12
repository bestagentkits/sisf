package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxManifestBytes bounds how much of a manifest file is read before its
// signature is known to be good.
const maxManifestBytes = 1 << 20

// SignedManifest is a release manifest with a detached signature over its
// SigningPayload. It is the file the supervisor reads at startup.
type SignedManifest struct {
	Manifest  ReleaseManifest `json:"manifest"`
	Signature core.Signature  `json:"signature,omitzero"`
}

// SignManifest signs m with the root key behind signer. An invalid manifest
// is never signed.
func SignManifest(ctx context.Context, signer core.Signer, m ReleaseManifest) (SignedManifest, error) {
	if err := m.Validate(); err != nil {
		return SignedManifest{}, err
	}
	sig, err := signer.Sign(ctx, m.SigningPayload())
	if err != nil {
		return SignedManifest{}, err
	}
	return SignedManifest{Manifest: m, Signature: sig}, nil
}

// Verify returns the manifest only when its signature is good under
// verifier. The bytes checked are rebuilt from the manifest as it was read,
// so what is returned is exactly what was signed: an edited, added or
// removed field changes those bytes and fails with SIGNATURE_INVALID, as
// does a manifest that carries no signature at all.
func (s SignedManifest) Verify(ctx context.Context, verifier core.Verifier) (ReleaseManifest, error) {
	if verifier == nil {
		return ReleaseManifest{}, rejected("there is no verifier to check the release manifest with")
	}
	if s.Signature.Validate() != nil {
		return ReleaseManifest{}, rejected("the release manifest is not signed")
	}
	if err := s.Manifest.Validate(); err != nil {
		return ReleaseManifest{}, err
	}
	if err := verifier.Verify(ctx, s.Manifest.SigningPayload(), s.Signature); err != nil {
		return ReleaseManifest{}, err
	}
	return s.Manifest, nil
}

// ParseSignedManifest decodes a manifest file. Decoding is strict: a field
// these types do not know, which no signature could cover, or anything after
// the document is refused. It does not verify the signature.
func ParseSignedManifest(data []byte) (SignedManifest, error) {
	var signed SignedManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signed); err != nil {
		return SignedManifest{}, invalid("the release manifest file is not a signed release manifest").WithCause(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return SignedManifest{}, invalid("the release manifest file holds more than one document")
	}
	return signed, nil
}

// ReadSignedManifest reads and decodes the manifest file at path. A missing
// file is NOT_FOUND. It does not verify the signature.
func ReadSignedManifest(path string) (SignedManifest, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SignedManifest{}, core.NewError(core.CodeNotFound, sprintf("signed release manifest %s does not exist", path))
	}
	if err != nil {
		return SignedManifest{}, unreadable(path, err)
	}
	defer func() { _ = file.Close() }() // read-only: a failed close loses nothing
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return SignedManifest{}, unreadable(path, err)
	}
	if len(data) > maxManifestBytes {
		return SignedManifest{}, invalid("release manifest file %s is larger than %d bytes", path, maxManifestBytes)
	}
	return ParseSignedManifest(data)
}

// WriteSignedManifest writes the manifest file at path as indented JSON. The
// file holds no secret.
func WriteSignedManifest(path string, signed SignedManifest) error {
	data, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return invalid("the signed release manifest cannot be encoded").WithCause(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return core.NewError(core.CodeUnavailable, sprintf("release manifest file %s cannot be written", path)).WithCause(err)
	}
	return nil
}

// unreadable builds the error for a manifest file that exists but cannot be read.
func unreadable(path string, cause error) *core.Error {
	return core.NewError(core.CodeUnavailable, sprintf("release manifest file %s cannot be read", path)).WithCause(cause)
}
