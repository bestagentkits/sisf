package appbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Store keeps verified bundles in a directory of the supervisor's zone, each
// as <hex>.tar, and streams one back by its digest. A bundle is only ever
// stored after Verify read it, and every stream is checked again against the
// digest it was opened by: a file changed on disk fails its upload rather
// than reaching a revision.
type Store struct {
	Dir string
}

// Put verifies the bundle r against limits, stores it, and returns its
// digest. Storing a bundle again is harmless.
func (s Store) Put(r io.Reader, limits Limits) (string, error) {
	tmp, err := os.CreateTemp(s.Dir, ".put-*.tar")
	if err != nil {
		return "", fmt.Errorf("store bundle: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename; a failed put leaves nothing
	digest, verr := Verify(io.TeeReader(r, tmp), limits)
	if cerr := tmp.Close(); verr == nil && cerr != nil {
		verr = cerr
	}
	if verr != nil {
		return "", verr
	}
	if err := os.Rename(tmp.Name(), s.path(digest)); err != nil {
		return "", fmt.Errorf("store bundle: %w", err)
	}
	return digest, nil
}

// Open streams the stored bundle of digest. The stream ends with an error,
// never with io.EOF, when its bytes are not the digest's.
func (s Store) Open(_ context.Context, digest string) (io.ReadCloser, error) {
	if !digestPattern.MatchString(digest) {
		return nil, fmt.Errorf("bundle digest %q is not sha256:<64 hex>", digest)
	}
	f, err := os.Open(s.path(digest))
	if err != nil {
		return nil, fmt.Errorf("open bundle: %w", err)
	}
	return &checkedStream{f: f, h: sha256.New(), want: strings.TrimPrefix(digest, "sha256:")}, nil
}

func (s Store) path(digest string) string {
	return filepath.Join(s.Dir, strings.TrimPrefix(digest, "sha256:")+".tar")
}

// checkedStream hashes what is read and turns the end of a stream whose
// hash differs into an error.
type checkedStream struct {
	f    *os.File
	h    hash.Hash
	want string
}

func (c *checkedStream) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.h.Write(p[:n])
	if errors.Is(err, io.EOF) && hex.EncodeToString(c.h.Sum(nil)) != c.want {
		return n, errors.New("the stored bundle does not match its digest")
	}
	return n, err
}

func (c *checkedStream) Close() error { return c.f.Close() }
