package appbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// Input is what Assemble builds a bundle from.
type Input struct {
	// RepoDir is the root of the candidate's checkout.
	RepoDir string
	// Bundle says where the lockfile is, what to prune and what to copy.
	Bundle managedapp.Bundle
	// Fetcher downloads archives that the cache has no verified copy of.
	Fetcher Fetcher
	// CacheDir keeps downloaded archives, named by their sha256.
	CacheDir string
	// OutputPath is where the bundle tar is written.
	OutputPath string
	Limits     Limits
}

// Result describes a bundle tar Assemble wrote.
type Result struct {
	// Digest is "sha256:<hex>" of the tar's bytes.
	Digest string
	// Files and Bytes count the bundle's regular files and their sizes.
	Files int
	Bytes int64
	// Lock is the lockfile the bundle was built from.
	Lock Lock
}

// Assemble builds the content bundle of the candidate in in.RepoDir: it
// unpacks every archive the lockfile pins, after verifying its sha256,
// removes the Prune paths, copies the Sources over, and writes the tree as
// a reproducible tar. The same repository content always gives the same
// digest. Nothing is executed.
func Assemble(ctx context.Context, in Input) (Result, error) {
	if err := in.Limits.validate(); err != nil {
		return Result{}, err
	}
	if in.Fetcher == nil || in.RepoDir == "" || in.CacheDir == "" || in.OutputPath == "" {
		return Result{}, errors.New("assemble: RepoDir, Fetcher, CacheDir and OutputPath are required")
	}
	repo, err := os.OpenRoot(in.RepoDir)
	if err != nil {
		return Result{}, fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = repo.Close() }()
	lock, err := readLock(repo, in.Bundle.Lock)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(in.CacheDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("cache: %w", err)
	}
	outDir := filepath.Dir(in.OutputPath)
	stageDir, err := os.MkdirTemp(outDir, ".bundle-stage-*")
	if err != nil {
		return Result{}, fmt.Errorf("staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	stage, err := os.OpenRoot(stageDir)
	if err != nil {
		return Result{}, fmt.Errorf("staging: %w", err)
	}
	defer func() { _ = stage.Close() }()

	if err := buildTree(ctx, in, repo, stage, lock); err != nil {
		return Result{}, err
	}
	res, err := writeOutput(stage, in.OutputPath, in.Limits)
	res.Lock = lock
	return res, err
}

// buildTree fills the staging tree: archives in canonical order, then
// Prune, then Sources in the order the Spec lists them.
func buildTree(ctx context.Context, in Input, repo, stage *os.Root, lock Lock) error {
	b := &budget{limits: in.Limits}
	for _, e := range lock.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := openVerified(ctx, in.Fetcher, in.CacheDir, e, in.Limits.MaxTotalBytes)
		if err != nil {
			return err
		}
		err = unpackArchive(ctx, stage, file, e, b)
		if err = errors.Join(err, file.Close()); err != nil {
			return fmt.Errorf("%s %s %s: %w", e.Kind, e.Slug, e.Version, err)
		}
	}
	for _, p := range in.Bundle.Prune {
		if err := prune(stage, p); err != nil {
			return err
		}
	}
	for _, src := range in.Bundle.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copySource(repo, stage, src, b); err != nil {
			return err
		}
	}
	return nil
}

// writeOutput streams the tar to a temporary file beside path, hashing it
// on the way, and renames it into place only when it is complete. It does
// not fsync: whoever consumes the tar checks it against Result.Digest
// (CheckDigest, Verify), which catches a file torn by a crash.
func writeOutput(stage *os.Root, path string, limits Limits) (Result, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bundle-*.tar")
	if err != nil {
		return Result{}, fmt.Errorf("output: %w", err)
	}
	h := sha256.New()
	files, n, err := writeTar(stage, io.MultiWriter(tmp, h), limits)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return Result{}, fmt.Errorf("output: %w", err)
	}
	return Result{Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Files: files, Bytes: n}, nil
}
