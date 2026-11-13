package appbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// lstatNoLinks returns the Lstat of p in repo after checking that neither p
// nor any directory on the way to it is a symbolic link. os.Root follows
// links that stay inside the root, so this is checked one component at a
// time.
func lstatNoLinks(repo *os.Root, p string) (fs.FileInfo, error) {
	parts := strings.Split(p, "/")
	var info fs.FileInfo
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		var err error
		if info, err = repo.Lstat(prefix); err != nil {
			return nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%q is a symbolic link", prefix)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%q is not a directory", prefix)
		}
	}
	return info, nil
}

// readLock reads and parses the lockfile at repository path p.
func readLock(repo *os.Root, p string) (Lock, error) {
	if err := checkRelative("bundle.lock", p); err != nil {
		return Lock{}, err
	}
	info, err := lstatNoLinks(repo, p)
	if err != nil {
		return Lock{}, fmt.Errorf("lockfile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Lock{}, fmt.Errorf("lockfile %q is not a regular file", p)
	}
	f, err := repo.Open(p)
	if err != nil {
		return Lock{}, fmt.Errorf("lockfile: %w", err)
	}
	lock, err := ParseLock(f)
	return lock, errors.Join(err, f.Close())
}

// copySource copies the repository file or directory src.From to the
// bundle path src.To. Every file it meets must be a plain file or
// directory; a symbolic link or special file anywhere refuses the source.
// A copied file replaces a file of the same name from an archive.
func copySource(repo, stage *os.Root, src managedapp.Source, b *budget) error {
	if err := checkRelative("source from", src.From); err != nil {
		return err
	}
	if err := checkRelative("source to", src.To); err != nil {
		return err
	}
	info, err := lstatNoLinks(repo, src.From)
	if err != nil {
		return fmt.Errorf("source %q: %w", src.From, err)
	}
	if info.Mode().IsRegular() {
		if err := stage.MkdirAll(path.Dir(src.To), 0o755); err != nil {
			return err
		}
		return copyRepoFile(repo, stage, src.From, src.To, info, b)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a plain file or directory", src.From)
	}
	return fs.WalkDir(repo.FS(), src.From, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := path.Join(src.To, strings.TrimPrefix(strings.TrimPrefix(p, src.From), "/"))
		switch {
		case d.IsDir():
			if err := b.entry(target); err != nil {
				return err
			}
			return stage.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return copyRepoFile(repo, stage, p, target, fi, b)
		default:
			return fmt.Errorf("source %q: %q is not a plain file or directory", src.From, p)
		}
	})
}

func copyRepoFile(repo, stage *os.Root, from, to string, info fs.FileInfo, b *budget) error {
	if err := b.entry(to); err != nil {
		return err
	}
	in, err := repo.Open(from)
	if err != nil {
		return err
	}
	// The open file must still be the regular file that was checked.
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() || !os.SameFile(st, info) {
		return errors.Join(fmt.Errorf("source %q changed while it was copied", from), err, in.Close())
	}
	return writeFile(stage, to, fileMode(info.Mode()), true, b, in)
}

// prune removes bundle path p; a path that does not exist is not an error.
func prune(stage *os.Root, p string) error {
	if err := checkRelative("prune", p); err != nil {
		return err
	}
	if err := stage.RemoveAll(p); err != nil {
		return fmt.Errorf("prune %q: %w", p, err)
	}
	return nil
}
