package programstore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// trustDir is a trust volume only its owner can write.
func trustDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func digestOf(data string) core.Digest {
	sum := sha256.Sum256([]byte(data))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// keep stores data as a program and returns its digest.
func keep(t *testing.T, s *Store, data string) core.Digest {
	t.Helper()
	in, err := s.Receive()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Abort()
	for _, part := range []string{data[:len(data)/2], data[len(data)/2:]} {
		if _, err := in.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := in.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// A received program is kept under the digest of exactly the bytes that
// were written, in a directory and a file only their owner can reach, and
// the file cannot be written even by its owner.
func TestAProgramIsKeptUnderTheDigestOfItsBytesForItsOwnerOnly(t *testing.T) {
	t.Parallel()
	trust := trustDir(t)
	s, err := Open(trust)
	if err != nil {
		t.Fatal(err)
	}
	digest := keep(t, s, "#!/bin/sh\nexit 0\n")
	if digest != digestOf("#!/bin/sh\nexit 0\n") {
		t.Fatalf("kept under %s, not the digest of the bytes", digest)
	}
	path, err := s.Path(digest)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(trust, "programs", "sha256-"+strings.TrimPrefix(string(digest), "sha256:")); path != want {
		resolved, _ := filepath.EvalSymlinks(want)
		if path != resolved {
			t.Errorf("the program is at %s, want %s", path, want)
		}
	}
	for name, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o500} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s has mode %v (%v), want %04o", name, info.Mode().Perm(), err, want)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the store holds %d entries after one program, want no intake left behind", len(entries))
	}
}

// A stored program is hashed again every time it is asked for: bytes that
// changed after they were received are refused as tampered, and a digest
// the store never received is not found.
func TestAProgramThatChangedAfterItWasReceivedIsRefused(t *testing.T) {
	t.Parallel()
	s, err := Open(trustDir(t))
	if err != nil {
		t.Fatal(err)
	}
	digest := keep(t, s, "the program QA judged")
	path, err := s.Path(digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path(digestOf("another program")); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("a digest never received: %v, want NOT_FOUND", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("what an agent swapped in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path(digest); !core.HasCode(err, core.CodeTampered) {
		t.Errorf("a program whose bytes changed: %v, want TAMPERED", err)
	}
	// A link in a program's place could lead anywhere.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path(digest); !core.HasCode(err, core.CodeTampered) {
		t.Errorf("a link in a program's place: %v, want TAMPERED", err)
	}
	for _, bad := range []core.Digest{"", "sha256:short", core.Digest("sha512:" + strings.Repeat("a", 128)), "sha256:../../../../bin/sh"} {
		if _, err := s.Path(bad); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%q: %v, want INVALID_ARGUMENT", bad, err)
		}
	}
}

// A program larger than the limit is refused while it is received, an
// empty one when it is committed, and neither leaves a file behind.
func TestAnOversizedOrEmptyProgramIsNotKept(t *testing.T) {
	t.Parallel()
	s, err := Open(trustDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s.maxBytes = 8
	in, err := s.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Write([]byte("12345678")); err != nil {
		t.Fatalf("a program of exactly the limit: %v", err)
	}
	if _, err := in.Write([]byte("9")); !core.HasCode(err, core.CodeLimitExceeded) {
		t.Fatalf("one byte past the limit: %v, want LIMIT_EXCEEDED", err)
	}
	if _, err := in.Commit(); err == nil {
		t.Error("an oversized program was committed")
	}
	in.Abort()
	empty, err := s.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Commit(); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an empty program: %v, want INVALID_ARGUMENT", err)
	}
	empty.Abort()
	if entries, _ := os.ReadDir(filepath.Join(s.root, dirName)); len(entries) != 0 {
		t.Errorf("%d files were left behind", len(entries))
	}
}

// Retain removes every program that is not named, and what an interrupted
// intake left; a store that was never written to has nothing to remove.
func TestOnlyTheNamedProgramsAreRetained(t *testing.T) {
	t.Parallel()
	s, err := Open(trustDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := s.Retain(); err != nil || len(removed) != 0 {
		t.Fatalf("an empty store: %v, %v", removed, err)
	}
	lkg, current, old := keep(t, s, "last known good"), keep(t, s, "current"), keep(t, s, "superseded")
	interrupted, err := s.Receive()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = interrupted.Write([]byte("half a program"))
	removed, err := s.Retain(lkg, current, digestOf("not in the store"))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v, want the superseded program and the interrupted intake", removed)
	}
	for digest, kept := range map[core.Digest]bool{lkg: true, current: true, old: false} {
		if _, err := s.Path(digest); (err == nil) != kept {
			t.Errorf("%s kept: %v, want %t", digest, err, kept)
		}
	}
}

// The store lives on the trust volume: a volume other accounts can write,
// or a program directory they can reach, is refused.
func TestAStoreOtherAccountsCanReachIsRefused(t *testing.T) {
	t.Parallel()
	open := trustDir(t)
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(open); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("a trust volume everyone can write: %v, want PERMISSION_DENIED", err)
	}
	trust := trustDir(t)
	if err := os.Mkdir(filepath.Join(trust, dirName), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("a program directory others can read: %v, want PERMISSION_DENIED", err)
	}
	if _, err := s.Path(digestOf("x")); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("a program directory others can read: %v, want PERMISSION_DENIED", err)
	}
}
