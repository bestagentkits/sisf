package appbundle

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// zipEntry is one entry of a test archive. A zero mode is a plain 0644
// file, or a directory when the name ends with "/".
type zipEntry struct {
	name string
	body string
	mode fs.FileMode
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			fh.SetMode(e.mode)
		} else if e.name[len(e.name)-1] != '/' {
			fh.SetMode(0o644)
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeFetcher serves archives from memory and counts fetches.
type fakeFetcher struct {
	mu    sync.Mutex
	files map[string][]byte
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, url string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	b, ok := f.files[url]
	if !ok {
		return nil, fmt.Errorf("no archive at %s", url)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// longName is a path over 255 bytes, too long even for a ustar prefix.
var longName = "deep/" + string(bytes.Repeat([]byte("d"), 120)) + "/" + string(bytes.Repeat([]byte("e"), 120)) + "/file.php"

func defaultLimits() Limits {
	return Limits{MaxTotalBytes: 1 << 20, MaxFiles: 200, MaxFileBytes: 64 << 10}
}

func coreEntries() []zipEntry {
	return []zipEntry{
		{name: "wordpress/"},
		{name: "wordpress/index.php", body: "<?php // index"},
		{name: "wordpress/wp-admin/"},
		{name: "wordpress/wp-admin/admin.php", body: "<?php // admin"},
		{name: "wordpress/wp-content/plugins/akismet/akismet.php", body: "<?php // akismet"},
		{name: "wordpress/wp-content/plugins/hello.php", body: "<?php // hello"},
		{name: "wordpress/wp-content/themes/plain/style.css", body: "/* plain */"},
		{name: "wordpress/bin/tool.sh", body: "#!/bin/sh\n", mode: 0o750},
		{name: "wordpress/wp-includes/blocks/archives.php", body: "<?php // archives"},
		{name: "wordpress/wp-includes/blocks/archives/block.json", body: "{}"},
		{name: "wordpress/" + longName, body: "<?php // deep"},
	}
}

func lockLine(kind, slug, version string, archive []byte) string {
	return fmt.Sprintf("%s %s %s %s\n", kind, slug, version, shaHex(archive))
}

// rawZip writes one deflated entry whose header declares declared bytes
// while it really holds actual zero bytes.
func rawZip(t *testing.T, name string, actual int, declared uint64) []byte {
	t.Helper()
	body := make([]byte, actual)
	var comp bytes.Buffer
	fw, err := flate.NewWriter(&comp, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(body),
		CompressedSize64: uint64(comp.Len()), UncompressedSize64: declared}
	fh.SetMode(0o644)
	w, err := zw.CreateRaw(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(comp.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
