package appbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Measure hashes the stored bundle of digest now and returns the digest of
// those bytes, NOT_FOUND when nothing is stored for it. It never returns
// the digest it was asked for unless the bytes have it.
func (s Store) Measure(digest string) (string, error) {
	if !digestPattern.MatchString(digest) {
		return "", core.NewError(core.CodeInvalidArgument, "bundle digest is not sha256:<64 hex>")
	}
	f, err := os.Open(s.path(digest))
	if errors.Is(err, fs.ErrNotExist) {
		return "", core.NewError(core.CodeNotFound, "no bundle is stored for "+digest)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// PairObserver measures what a backend would start for a bundle artifact:
// the pair of its runtime image, which the daemon resolves by the digest it
// is pinned to and checks as content, and the bundle bytes the store holds
// now. It observes no image-only artifact.
type PairObserver struct {
	Store Store
}

// ObserveArtifact returns the pair digest of the artifact's image and the
// measured bundle; a changed stored bundle yields another digest than the
// artifact's, which the release refuses as tampered.
func (o PairObserver) ObserveArtifact(_ context.Context, a deploy.Artifact) (core.ReleaseDigest, error) {
	if a.Bundle == "" {
		return "", core.NewError(core.CodeNotFound, "artifact "+a.Unit+" has no bundle; this observer measures bundle artifacts only")
	}
	measured, err := o.Store.Measure(a.Bundle)
	if err != nil {
		return "", err
	}
	return deploy.PairDigest(a.Image, measured), nil
}
