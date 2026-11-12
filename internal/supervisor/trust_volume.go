package supervisor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// othersCanWrite are the permission bits that let the group or everyone
// write a directory.
const othersCanWrite fs.FileMode = 0o022

// resolveTrustDir returns the real location of the trust volume, with
// symbolic links followed. The volume must already exist as a directory, and
// must not be writable by the group or by everyone: a directory other
// accounts can write is not supervisor-only, whatever the manifest declares.
func resolveTrustDir(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", invalid("trust volume path %q cannot be resolved", dir).WithCause(err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if errors.Is(err, fs.ErrNotExist) {
		return "", newError(core.CodeNotFound, "trust volume %s does not exist", dir)
	}
	if err != nil {
		return "", newError(core.CodeUnavailable, "trust volume %s cannot be inspected", dir).WithCause(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", newError(core.CodeUnavailable, "trust volume %s cannot be inspected", dir).WithCause(err)
	}
	if !info.IsDir() {
		return "", invalid("trust volume %s is not a directory", dir)
	}
	// Permission bits say nothing about write access on Windows.
	if runtime.GOOS != "windows" && info.Mode().Perm()&othersCanWrite != 0 {
		return "", newError(core.CodePermissionDenied,
			"trust volume %s has mode %04o, which lets other accounts write it; only the supervisor may", dir, info.Mode().Perm())
	}
	return resolved, nil
}

// resolveInside returns the real location of target when it is a place for a
// plain file inside root, the resolved trust volume. Symbolic links are
// followed before the comparison, so neither ".." nor a link can lead the
// record somewhere another zone can write. what names the file in errors.
func resolveInside(root, target, what string) (string, error) {
	outside := newError(core.CodePermissionDenied,
		"%s %s is not on the trust volume; it may only be kept where no other zone can write", what, target)
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", outside.WithCause(err)
	}
	// The file may not exist yet, so its directory is what gets resolved.
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", outside.WithCause(err)
	}
	resolved := filepath.Join(parent, filepath.Base(absolute))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", outside.WithCause(err)
	}
	// An existing entry must be a plain file: a link would be followed on
	// open and could point anywhere.
	if info, err := os.Lstat(resolved); err == nil && !info.Mode().IsRegular() {
		return "", newError(core.CodePermissionDenied, "%s %s is not a plain file on the trust volume", what, target)
	}
	return resolved, nil
}

// TrustPath returns the real location of the file name inside the trust
// volume trustDir. It refuses a volume that other accounts can write and a
// name that leads out of the volume or to anything but a plain file, so a
// record kept there cannot be reached by another zone.
func TrustPath(trustDir, name string) (string, error) {
	root, err := resolveTrustDir(trustDir)
	if err != nil {
		return "", err
	}
	return resolveInside(root, filepath.Join(root, name), "trust record")
}

// WriteTrustFile replaces the file at path, a location TrustPath returned.
// The new content is written beside the old one and renamed over it, so a
// crash leaves either the old file or the new one, never a partial file.
// The file is readable and writable by its owner only.
func WriteTrustFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".trust-*.tmp")
	if err != nil {
		return newError(core.CodeUnavailable, "the trust volume cannot be written").WithCause(err)
	}
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(temp.Name()) // best effort: the write already failed and is reported
		return newError(core.CodeUnavailable, "trust record %s cannot be written", path).WithCause(err)
	}
	return nil
}
