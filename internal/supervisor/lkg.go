package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxLKGBytes bounds how much of an LKG record is read.
const maxLKGBytes = 1 << 20

// LKGRecord names the last known good release: the one to return to when a
// later release must be undone. It carries the signed release manifest
// itself, so the record can always be checked against the root key and can
// never point at digests the Owner did not sign.
type LKGRecord struct {
	Release    signing.SignedManifest `json:"release"`
	RecordedAt time.Time              `json:"recorded_at"`
}

// LKGStore keeps the LKG record in one file on the trust volume. Every
// record that enters or leaves it is a release manifest of this factory with
// a good signature.
type LKGStore struct {
	path     string
	factory  core.FactoryID
	verifier core.Verifier
}

// OpenLKGStore returns the store for the record at path. It is the only way
// to obtain a store, and it refuses a path that does not resolve to a plain
// file inside trustDir, so an LKG record cannot be kept anywhere a release
// candidate or another zone could write.
func OpenLKGStore(trustDir, path string, factory core.FactoryID, verifier core.Verifier) (*LKGStore, error) {
	if err := factory.Validate(); err != nil {
		return nil, err
	}
	if verifier == nil {
		return nil, invalid("the LKG store needs a verifier for release manifests")
	}
	root, err := resolveTrustDir(trustDir)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveInside(root, path, "LKG record")
	if err != nil {
		return nil, err
	}
	return &LKGStore{path: resolved, factory: factory, verifier: verifier}, nil
}

// Path is the resolved location of the record.
func (s *LKGStore) Path() string { return s.path }

// Load returns the record, or false when none has been written. A record
// that cannot be decoded or does not verify is reported as TAMPERED: only
// the supervisor writes this file, so anything else in it was put there.
func (s *LKGStore) Load(ctx context.Context) (LKGRecord, bool, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return LKGRecord{}, false, nil
	}
	if err != nil {
		return LKGRecord{}, false, newError(core.CodeUnavailable, "LKG record %s cannot be read", s.path).WithCause(err)
	}
	defer func() { _ = file.Close() }() // read-only: a failed close loses nothing
	data, err := io.ReadAll(io.LimitReader(file, maxLKGBytes+1))
	if err != nil {
		return LKGRecord{}, false, newError(core.CodeUnavailable, "LKG record %s cannot be read", s.path).WithCause(err)
	}
	// Decoding is strict: one document, with no field the supervisor never writes.
	var record LKGRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > maxLKGBytes || decoder.Decode(&record) != nil {
		return LKGRecord{}, false, newError(core.CodeTampered, "LKG record %s is not a record the supervisor wrote", s.path)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return LKGRecord{}, false, newError(core.CodeTampered, "LKG record %s holds more than one document", s.path)
	}
	if err := s.check(ctx, record); err != nil {
		return LKGRecord{}, false, newError(core.CodeTampered, "LKG record %s cannot be trusted", s.path).WithCause(err)
	}
	return record, true, nil
}

// Save replaces the record. The new record is written beside the old one and
// renamed over it, so a crash leaves either the old record or the new one,
// never a partial file.
func (s *LKGStore) Save(ctx context.Context, record LKGRecord) error {
	if err := s.check(ctx, record); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return invalid("the LKG record cannot be encoded").WithCause(err)
	}
	// CreateTemp makes the file readable and writable by its owner only.
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".lkg-*.tmp")
	if err != nil {
		return newError(core.CodeUnavailable, "the trust volume cannot be written").WithCause(err)
	}
	_, err = temp.Write(append(data, '\n'))
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), s.path)
	}
	if err != nil {
		_ = os.Remove(temp.Name()) // best effort: the write already failed and is reported
		return newError(core.CodeUnavailable, "LKG record %s cannot be written", s.path).WithCause(err)
	}
	return nil
}

// check requires a record to carry a release manifest of this factory whose
// signature verifies, and the time it was recorded.
func (s *LKGStore) check(ctx context.Context, record LKGRecord) error {
	release, err := record.Release.Verify(ctx, s.verifier)
	if err != nil {
		return err
	}
	if release.FactoryID != s.factory {
		return newError(core.CodePermissionDenied, "the LKG release is of factory %q, not of this factory %q", release.FactoryID, s.factory)
	}
	if record.RecordedAt.IsZero() {
		return invalid("the LKG record must say when it was recorded")
	}
	return nil
}
