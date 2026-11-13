package appbundle

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

// tarOverheadPerEntry bounds the header bytes one honest entry takes: a
// header block, a PAX header block and a PAX record for a long path.
const tarOverheadPerEntry = 4 * 512

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Verify reads a bundle tar that a candidate could have influenced and
// returns "sha256:<hex>" of exactly the bytes it read. It refuses, rather
// than skips, any entry that is not a plain file or directory (links,
// devices, fifos, sparse files), any name that is absolute, unclean or
// climbs with "..", a name seen twice, a file where another entry needs a
// directory, a mode with setuid, setgid or sticky bits, extended records
// other than a long path, a missing end marker, non-zero bytes after it,
// and anything past limits.
func Verify(r io.Reader, limits Limits) (string, error) {
	if err := limits.validate(); err != nil {
		return "", err
	}
	// Bound the whole stream so header tricks cannot make it unbounded.
	maxStream := limits.MaxTotalBytes + int64(limits.MaxFiles+1)*tarOverheadPerEntry + maxTrailerBytes
	h := sha256.New()
	counted := &countingReader{r: io.LimitReader(r, maxStream+1)}
	tee := io.TeeReader(counted, h)
	tr := tar.NewReader(tee)
	b := budget{limits: limits}
	seen := names{entries: map[string]bool{}, implied: map[string]bool{}}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read bundle: %w", streamErr(err, counted, maxStream))
		}
		if err := checkHeader(hdr, &seen, &b); err != nil {
			return "", fmt.Errorf("bundle entry %q: %w", hdr.Name, err)
		}
		if n, err := io.Copy(io.Discard, tr); err != nil || n != hdr.Size {
			return "", fmt.Errorf("bundle entry %q: short or bad content: %w", hdr.Name, streamErr(err, counted, maxStream))
		}
	}
	// archive/tar also reports io.EOF for a stream cut at a block boundary;
	// only a stream that ended with the two-zero-block marker is whole.
	if counted.n%512 != 0 || !counted.endsWithMarker() {
		return "", fmt.Errorf("read bundle: %w: no end-of-archive marker", io.ErrUnexpectedEOF)
	}
	if err := checkTrailer(tee); err != nil {
		return "", err
	}
	if counted.n > maxStream {
		return "", fmt.Errorf("%w: bundle stream is larger than %d bytes", errLimit, maxStream)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// names tracks the entries of a tar so far: each name with whether it is a
// directory, and every directory some entry's name implies.
type names struct {
	entries map[string]bool
	implied map[string]bool
}

// add records name, refusing a name seen before and a file where another
// entry, before or after it, needs a directory.
func (s *names) add(name string, isDir bool) error {
	if _, dup := s.entries[name]; dup {
		return errors.New("appears twice")
	}
	if !isDir && s.implied[name] {
		return errors.New("is a file but other entries sit below it")
	}
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		if isDirEntry, ok := s.entries[dir]; ok && !isDirEntry {
			return fmt.Errorf("is nested under the file %q", dir)
		}
		s.implied[dir] = true
	}
	s.entries[name] = isDir
	return nil
}

func checkHeader(hdr *tar.Header, seen *names, b *budget) error {
	isDir := hdr.Typeflag == tar.TypeDir
	if !isDir && hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("type %q is not a plain file or directory", hdr.Typeflag)
	}
	name := hdr.Name
	if isDir {
		name = strings.TrimSuffix(name, "/")
	}
	if err := checkRelative("name", name); err != nil {
		return err
	}
	if hdr.Linkname != "" {
		return errors.New("has a link name")
	}
	if hdr.Mode&^0o777 != 0 {
		return fmt.Errorf("mode %o has bits beyond permissions", hdr.Mode)
	}
	for k := range hdr.PAXRecords {
		if k != "path" {
			return fmt.Errorf("extended record %q is not allowed", k)
		}
	}
	if err := seen.add(name, isDir); err != nil {
		return err
	}
	if err := b.entry(name); err != nil {
		return err
	}
	if isDir {
		if hdr.Size != 0 {
			return errors.New("directory has content")
		}
		return nil
	}
	if hdr.Size < 0 {
		return errors.New("negative size")
	}
	return b.fileBytes(name, hdr.Size)
}

// CheckDigest checks that the file at path has digest want, "sha256:<hex>".
func CheckDigest(path, want string) error {
	if !digestPattern.MatchString(want) {
		return fmt.Errorf("digest %q is not sha256:<64 lowercase hex>", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("digest %s: %w", path, err)
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s has digest %s, want %s", path, got, want)
	}
	return nil
}
