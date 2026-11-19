package appbundle

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storedBundle(t *testing.T) (Store, string, []byte) {
	t.Helper()
	data := makeTar(t, &tar.Header{Name: "index.php", Mode: 0o644, Size: 0, Typeflag: tar.TypeReg})
	s := Store{Dir: t.TempDir()}
	digest, err := s.Put(bytes.NewReader(data), defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return s, digest, data
}

func TestAStoredBundleStreamsBackByItsDigest(t *testing.T) {
	t.Parallel()
	s, digest, data := storedBundle(t)
	if digest != "sha256:"+shaHex(data) {
		t.Fatalf("digest %s", digest)
	}
	r, err := s.Open(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
}

// A bundle changed on disk after it was stored never ends as a clean
// stream, so its upload fails and no revision mounts it.
func TestATamperedBundleFailsItsStream(t *testing.T) {
	t.Parallel()
	s, digest, data := storedBundle(t)
	tampered := append(bytes.Clone(data), 0)
	if err := os.WriteFile(filepath.Join(s.Dir, strings.TrimPrefix(digest, "sha256:")+".tar"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := io.ReadAll(r); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a tampered bundle streamed cleanly: %v", err)
	}
}

func TestTheStoreRefusesWhatItCannotVouchFor(t *testing.T) {
	t.Parallel()
	s := Store{Dir: t.TempDir()}
	if _, err := s.Put(strings.NewReader("not a tar"), defaultLimits()); err == nil {
		t.Fatal("an invalid bundle was stored")
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("a refused put left %v", entries)
	}
	for _, d := range []string{"sha256:../../etc/passwd", "md5:" + strings.Repeat("a", 32), ""} {
		if _, err := s.Open(context.Background(), d); err == nil {
			t.Fatalf("opened %q", d)
		}
	}
	if _, err := s.Open(context.Background(), "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("opened a bundle that was never stored")
	}
}
