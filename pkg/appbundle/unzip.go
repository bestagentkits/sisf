package appbundle

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// fileMode is the one mode a bundle file is given: 0755 when the original
// had any exec bit, 0644 otherwise.
func fileMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// unpackArchive unpacks a verified archive of entry e into the staging
// tree. Every entry must be a plain file or directory under the kind's
// Strip directory, with a safe name; anything else refuses the whole
// archive. Sizes are counted while reading, not taken from headers.
func unpackArchive(ctx context.Context, stage *os.Root, file *os.File, e Entry, b *budget) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(file, info.Size())
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	rule := e.rule()
	strip, into := e.expand(rule.Strip), e.expand(rule.Into)
	if into != "" {
		// A pinned archive replaces what an earlier one (the core) put there.
		if err := stage.RemoveAll(strings.TrimSuffix(into, "/")); err != nil {
			return fmt.Errorf("replace %s: %w", into, err)
		}
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unpackEntry(stage, f, strip, into, b); err != nil {
			return fmt.Errorf("archive entry %q: %w", f.Name, err)
		}
	}
	return nil
}

func unpackEntry(stage *os.Root, f *zip.File, strip, into string, b *budget) error {
	isDir := strings.HasSuffix(f.Name, "/")
	name := strings.TrimSuffix(f.Name, "/")
	if err := checkRelative("name", name); err != nil {
		return err
	}
	if f.Name == strip {
		return nil // the top directory itself; its content lands in into
	}
	if !strings.HasPrefix(f.Name, strip) {
		return fmt.Errorf("is outside the archive's %s directory", strip)
	}
	mode := f.Mode()
	switch {
	case isDir && mode.IsDir():
	case !isDir && mode.IsRegular():
	default:
		return fmt.Errorf("is not a plain file or directory (mode %s)", mode)
	}
	target := into + strings.TrimPrefix(name, strip)
	if err := b.entry(target); err != nil {
		return err
	}
	if isDir {
		return stage.MkdirAll(target, 0o755)
	}
	if f.UncompressedSize64 > uint64(b.limits.MaxFileBytes) {
		return fmt.Errorf("%w: declares %d bytes, a file may have %d", errLimit, f.UncompressedSize64, b.limits.MaxFileBytes)
	}
	if err := stage.MkdirAll(path.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	return writeFile(stage, target, fileMode(mode), false, b, rc)
}

// writeFile creates target from src, which it always closes, and counts
// its size against b. Unless replace is set, target must not exist yet: two
// entries of one archive never share a name. It fails, rather than
// truncates, once src gives more than the budget allows for one file.
func writeFile(stage *os.Root, target string, mode fs.FileMode, replace bool, b *budget, src io.ReadCloser) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if replace {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := stage.OpenFile(target, flags, mode)
	if err != nil {
		return errors.Join(err, src.Close())
	}
	// Set the mode explicitly: OpenFile's is masked by the umask, and an
	// existing file keeps its old mode through O_TRUNC.
	err = out.Chmod(mode)
	var n int64
	if err == nil {
		n, err = copyCapped(out, src, b.nextFileCap(), target)
	}
	if err := errors.Join(err, src.Close(), out.Close()); err != nil {
		return err
	}
	return b.fileBytes(target, n)
}
