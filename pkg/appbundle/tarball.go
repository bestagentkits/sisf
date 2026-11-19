package appbundle

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"time"
)

// bundleTime is the modification time of every entry of a bundle tar.
var bundleTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// stagedEntry is one file or directory of the staging tree.
type stagedEntry struct {
	name string
	dir  bool
	mode fs.FileMode
	size int64
}

// writeTar writes the staging tree as a reproducible tar: paths sorted
// bytewise, owner 0:0 with no names, directories 0755, files 0644 or 0755,
// one fixed time, and no extended header unless a path is too long for a
// plain one. The tree is counted against limits once more, as Verify will
// count the tar. It returns the number of files and their bytes.
func writeTar(stage *os.Root, w io.Writer, limits Limits) (int, int64, error) {
	var entries []stagedEntry
	err := fs.WalkDir(stage.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("staged %q is not a plain file or directory", p)
		}
		entries = append(entries, stagedEntry{name: p, dir: d.IsDir(), mode: info.Mode(), size: info.Size()})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	b := budget{limits: limits}
	tw := tar.NewWriter(w)
	files := 0
	for _, e := range entries {
		if err := b.entry(e.name); err != nil {
			return 0, 0, err
		}
		hdr := &tar.Header{Name: e.name, ModTime: bundleTime}
		if e.dir {
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, e.name+"/", 0o755
		} else {
			if err := b.fileBytes(e.name, e.size); err != nil {
				return 0, 0, err
			}
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, int64(fileMode(e.mode)), e.size
			files++
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return 0, 0, fmt.Errorf("tar %q: %w", e.name, err)
		}
		if !e.dir {
			if err := copyStaged(stage, tw, e); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return 0, 0, err
	}
	return files, b.bytes, nil
}

func copyStaged(stage *os.Root, w io.Writer, e stagedEntry) error {
	f, err := stage.Open(e.name)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(f, e.size))
	err = errors.Join(err, f.Close())
	if err == nil && n != e.size {
		err = fmt.Errorf("staged %q changed size while it was packed", e.name)
	}
	return err
}
