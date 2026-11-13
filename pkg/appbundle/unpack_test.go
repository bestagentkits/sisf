package appbundle

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTar(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reg(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size}
}

func dir(name string) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}
}

func TestVerifyAcceptsAPlainBundle(t *testing.T) {
	t.Parallel()
	// A directory may come after the entries inside it.
	data := makeTar(t, dir("a/"), reg("a/b.php", 10), reg(longName, 3), reg("z/y", 1), dir("z/"))
	// Tools pad to a 10 KiB record with zeros; that is still the same bundle.
	data = append(data, make([]byte, 4096)...)
	got, err := Verify(bytes.NewReader(data), defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if want := "sha256:" + shaHex(data); got != want {
		t.Fatalf("digest %s, want the sha256 of every byte, %s", got, want)
	}
}

func TestVerifyRefuses(t *testing.T) {
	t.Parallel()
	small := Limits{MaxTotalBytes: 100, MaxFiles: 3, MaxFileBytes: 60}
	for name, tc := range map[string]struct {
		data   func(t *testing.T) []byte
		limits Limits
		want   string
	}{
		"symlink":      {tarOf(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}), small, "not a plain file"},
		"hardlink":     {tarOf(reg("a", 1), &tar.Header{Name: "l", Typeflag: tar.TypeLink, Linkname: "a"}), small, "not a plain file"},
		"char device":  {tarOf(&tar.Header{Name: "c", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}), small, "not a plain file"},
		"block device": {tarOf(&tar.Header{Name: "b", Typeflag: tar.TypeBlock, Devmajor: 8}), small, "not a plain file"},
		"fifo":         {tarOf(&tar.Header{Name: "p", Typeflag: tar.TypeFifo}), small, "not a plain file"},
		"climbs":       {tarOf(reg("a/../../x", 1)), small, "climbs"},
		"absolute":     {tarOf(reg("/etc/cron.d/x", 1)), small, "absolute"},
		"unclean":      {tarOf(reg("./a", 1)), small, "not a clean path"},
		"file as dir":  {retyped(dir("a/"), tar.TypeReg), small, "not a clean path"},
		"gnu sparse":   {retyped(reg("s", 0), tar.TypeGNUSparse), small, "read bundle"},
		"legacy file":  {retyped(dir("a/"), 0), small, "twice"}, // the pre-POSIX regular-file flag
		"duplicate":    {tarOf(reg("a", 1), reg("a", 1)), small, "twice"},
		"dir and file": {tarOf(dir("a/"), reg("a", 1)), small, "twice"},
		"under a file": {tarOf(reg("a", 1), reg("a/b", 1)), small, "nested under the file"},
		"file after":   {tarOf(reg("a/b", 1), reg("a", 1)), small, "other entries sit below it"},
		"dir content":  {retyped(reg("d", 5), tar.TypeDir), small, "directory has content"},
		"link name":    {tarOf(&tar.Header{Name: "x", Typeflag: tar.TypeReg, Mode: 0o644, Linkname: "y"}), small, "link name"},
		"huge padding": {func(t *testing.T) []byte {
			return append(makeTar(t, reg("a", 1)), make([]byte, maxTrailerBytes+1024)...)
		}, small, "padding"},
		"setuid":       {tarOf(&tar.Header{Name: "s", Typeflag: tar.TypeReg, Mode: 0o4755}), small, "beyond permissions"},
		"pax record":   {tarOf(&tar.Header{Name: "x", Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.xattr.user.x": "y"}}), small, "extended record"},
		"file too big": {tarOf(reg("a", 61)), small, "larger than 60"},
		"claims a huge size": {func(t *testing.T) []byte {
			var buf bytes.Buffer // only the header: the size is refused before any content is read
			if err := tar.NewWriter(&buf).WriteHeader(reg("a", 1<<32)); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}, small, "larger than 60"},
		"total too big":  {tarOf(reg("a", 60), reg("b", 41)), small, "larger than 100"},
		"too many":       {tarOf(dir("a/"), dir("b/"), dir("c/"), dir("d/")), small, "more than 3"},
		"trailing data":  {func(t *testing.T) []byte { return append(makeTar(t, reg("a", 1)), []byte("evil")...) }, small, "after its end marker"},
		"cut in content": {func(t *testing.T) []byte { d := makeTar(t, reg("a", 50)); return d[:530] }, small, "short or bad content"},
		"cut in padding": {func(t *testing.T) []byte { d := makeTar(t, reg("a", 50)); return d[:600] }, small, "end-of-archive"},
		"cut at a block": {func(t *testing.T) []byte { d := makeTar(t, reg("a", 50)); return d[:1024] }, small, "end-of-archive"},
		"empty":          {func(*testing.T) []byte { return nil }, small, "end-of-archive"},
		"not a tar":      {func(*testing.T) []byte { return []byte(strings.Repeat("garbage!", 100)) }, small, "read bundle"},
		"zero limits":    {tarOf(reg("a", 1)), Limits{}, "positive"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Verify(bytes.NewReader(tc.data(t)), tc.limits)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}
}

func tarOf(hdrs ...*tar.Header) func(*testing.T) []byte {
	return func(t *testing.T) []byte { return makeTar(t, hdrs...) }
}

// retyped writes a one-entry tar of h, then rewrites the entry's type flag
// to typeflag, which archive/tar would not write for that header, and
// fixes the header checksum. The entry is preceded by a regular file "a"
// so a retyped directory "a/" also collides by name.
func retyped(h *tar.Header, typeflag byte) func(*testing.T) []byte {
	return func(t *testing.T) []byte {
		data := makeTar(t, reg("a", 0), h)
		block := data[512:1024]
		block[156] = typeflag
		copy(block[148:156], "        ")
		sum := 0
		for _, c := range block {
			sum += int(c)
		}
		copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
		return data
	}
}

func TestCheckDigest(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "b.tar")
	if err := os.WriteFile(p, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := "sha256:" + shaHex([]byte("bundle"))
	if err := CheckDigest(p, good); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sha256:" + shaHex([]byte("other")), "sha256:ABC", strings.TrimPrefix(good, "sha256:"), "sha512:" + shaHex(nil)} {
		if err := CheckDigest(p, want); err == nil {
			t.Errorf("CheckDigest accepted %q", want)
		}
	}
	if err := CheckDigest(filepath.Join(t.TempDir(), "missing"), good); err == nil {
		t.Error("CheckDigest accepted a missing file")
	}
}
