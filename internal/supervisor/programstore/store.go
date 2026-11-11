// Package programstore keeps the programs candidate builds export to the
// supervisor of a Standalone factory, which its local deploy backend runs.
//
// The store is content-addressed: a program is kept under the sha256 of
// the bytes the supervisor received, so the name of a file is the digest
// the build record, the QA receipt and the signed artifact carry. It lives
// on the trust volume, in a directory only the supervisor's account can
// reach, and only the supervisor writes it. A program is hashed again every
// time it is asked for, so bytes that changed after they were received are
// never run.
//
// Under trusted_code_only the agents run as the supervisor's own
// operating-system user and could write the store; the hash before every
// start is what stands between such a write and a running program (see
// docs/threat-model.md).
package programstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// MaxProgramBytes is the largest program the store receives.
	MaxProgramBytes = 256 << 20
	// dirName is the store's directory on the trust volume.
	dirName = "programs"
	// dirMode and programMode let the owner alone reach the store, and
	// nobody write a program once it is kept.
	dirMode     fs.FileMode = 0o700
	programMode fs.FileMode = 0o500
	// intakePattern names a program while it is being received.
	intakePattern = ".intake-*"
	filePrefix    = "sha256-"
)

var sha256Digest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Store is the program store of one supervisor.
type Store struct {
	// root is the resolved trust volume.
	root string
	// MaxBytes is the largest program the store receives; MaxProgramBytes
	// unless a caller lowers it.
	MaxBytes int64
}

// Open returns the store on the trust volume trustDir, which must exist
// and be writable by its owner alone. It writes nothing: the store's
// directory is made when the first program is received.
func Open(trustDir string) (*Store, error) {
	probe, err := supervisor.TrustPath(trustDir, ".program-store")
	if err != nil {
		return nil, err
	}
	return &Store{root: filepath.Dir(probe), MaxBytes: MaxProgramBytes}, nil
}

// dir returns the store's directory, made when create is set. A directory
// that is a link, or that any other account can reach, is refused: it is
// not the supervisor's alone.
func (s *Store) dir(create bool) (string, error) {
	dir := filepath.Join(s.root, dirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if !create {
			return "", core.NewError(core.CodeNotFound, "the program store holds no program")
		}
		if err := os.Mkdir(dir, dirMode); err != nil {
			return "", core.NewError(core.CodeUnavailable, "the program store cannot be created").WithCause(err)
		}
		// Mkdir applies the process's umask, which can only remove bits.
		return dir, nil
	}
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the program store cannot be inspected").WithCause(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", core.NewError(core.CodePermissionDenied,
			"the program store "+dir+" is not a directory only its owner can reach; only the supervisor may write it")
	}
	return dir, nil
}

// name is the file name of the program with digest.
func name(digest core.Digest) (string, error) {
	if !sha256Digest.MatchString(string(digest)) {
		return "", core.NewError(core.CodeInvalidArgument, "a program is named by a sha256 digest")
	}
	return filePrefix + strings.TrimPrefix(string(digest), "sha256:"), nil
}

// Intake is one program being received.
type Intake struct {
	store *Store
	file  *os.File
	hash  hash.Hash
	size  int64
	// over is set once more than the limit was written.
	over bool
	done bool
}

// Receive starts receiving a program. The caller writes its bytes and then
// commits or aborts.
func (s *Store) Receive() (*Intake, error) {
	dir, err := s.dir(true)
	if err != nil {
		return nil, err
	}
	// CreateTemp makes the file for its owner only.
	file, err := os.CreateTemp(dir, intakePattern)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the program store cannot be written").WithCause(err)
	}
	return &Intake{store: s, file: file, hash: sha256.New()}, nil
}

// Write adds bytes to the program. Past the limit nothing more is written
// and LIMIT_EXCEEDED is returned.
func (in *Intake) Write(p []byte) (int, error) {
	if in.done {
		return 0, core.NewError(core.CodeInvalidArgument, "the program was already committed or aborted")
	}
	if in.over || in.size+int64(len(p)) > in.store.MaxBytes {
		in.over = true
		return 0, core.NewError(core.CodeLimitExceeded, "the program is larger than the store receives")
	}
	n, err := in.file.Write(p)
	in.hash.Write(p[:n])
	in.size += int64(n)
	if err != nil {
		return n, core.NewError(core.CodeUnavailable, "the program store cannot be written").WithCause(err)
	}
	return n, nil
}

// Size is the number of bytes received so far.
func (in *Intake) Size() int64 { return in.size }

// Digest is the sha256 of the bytes received so far.
func (in *Intake) Digest() core.Digest {
	return core.Digest("sha256:" + hex.EncodeToString(in.hash.Sum(nil)))
}

// Commit keeps the received bytes under their digest, replacing a program
// already kept under it, and returns the digest. An empty or oversized
// program is not kept.
func (in *Intake) Commit() (core.Digest, error) {
	switch {
	case in.done:
		return "", core.NewError(core.CodeInvalidArgument, "the program was already committed or aborted")
	case in.over:
		return "", core.NewError(core.CodeLimitExceeded, "the program is larger than the store receives")
	case in.size == 0:
		return "", core.NewError(core.CodeInvalidArgument, "an empty file is not a program")
	}
	digest := in.Digest()
	file, err := name(digest)
	if err != nil {
		return "", err
	}
	err = in.file.Sync()
	if err == nil {
		err = in.file.Chmod(programMode)
	}
	if closeErr := in.file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(in.file.Name(), filepath.Join(filepath.Dir(in.file.Name()), file))
	}
	in.done = true
	if err != nil {
		_ = os.Remove(in.file.Name()) // best effort: the failure is reported
		return "", core.NewError(core.CodeUnavailable, "the program cannot be kept in the store").WithCause(err)
	}
	return digest, nil
}

// Abort drops a program that was not committed. It does nothing after
// Commit or a second time.
func (in *Intake) Abort() {
	if in.done {
		return
	}
	in.done = true
	_ = in.file.Close()
	_ = os.Remove(in.file.Name()) // Retain removes what this misses
}

// Path returns the file of the program with digest, after hashing it
// again: NOT_FOUND when the store holds no such program, TAMPERED when what
// it holds under that name is not a plain file with exactly those bytes.
func (s *Store) Path(digest core.Digest) (string, error) {
	file, err := name(digest)
	if err != nil {
		return "", err
	}
	dir, err := s.dir(false)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, file)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", core.NewError(core.CodeNotFound, "the program store holds no program "+string(digest))
	}
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the program "+string(digest)+" cannot be inspected").WithCause(err)
	}
	tampered := core.NewError(core.CodeTampered,
		"the program store's file for "+string(digest)+" is not the program that was received; it is not run")
	if !info.Mode().IsRegular() {
		return "", tampered
	}
	f, err := os.Open(path)
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the program "+string(digest)+" cannot be read").WithCause(err)
	}
	defer func() { _ = f.Close() }() // read-only: a failed close loses nothing
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the program "+string(digest)+" cannot be read").WithCause(err)
	}
	if "sha256:"+hex.EncodeToString(sum.Sum(nil)) != string(digest) {
		return "", tampered
	}
	return path, nil
}

// Retain removes everything in the store but the programs named in keep,
// what an interrupted intake left included, and returns the names of what
// it removed. It must not run while a program is being received.
func (s *Store) Retain(keep ...core.Digest) ([]string, error) {
	dir, err := s.dir(false)
	if core.HasCode(err, core.CodeNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, digest := range keep {
		if file, err := name(digest); err == nil {
			kept[file] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the program store cannot be listed").WithCause(err)
	}
	var removed []string
	for _, e := range entries {
		if kept[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return removed, core.NewError(core.CodeUnavailable, "a program that is no longer needed cannot be removed").WithCause(err)
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}
