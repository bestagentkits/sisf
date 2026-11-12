package local

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// Files gives access to the sandbox's workspace and nothing outside it:
// every name is opened through an os.Root, so neither "..", an absolute
// path nor a symbolic link leads out.
func (b *Backend) Files(_ context.Context, id execution.SandboxID) (execution.FileSystem, error) {
	sb, err := b.running(id)
	if err != nil {
		return nil, err
	}
	return workspace{dir: sb.dir}, nil
}

type workspace struct{ dir string }

func (w workspace) open() (*os.Root, error) {
	root, err := os.OpenRoot(w.dir)
	if err != nil {
		return nil, core.NewError(core.CodeNotFound, "the sandbox workspace is gone").WithCause(err)
	}
	return root, nil
}

// ReadFile opens a file of the workspace.
func (w workspace) ReadFile(_ context.Context, name string) (io.ReadCloser, error) {
	root, err := w.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, fileError(err)
	}
	return f, nil
}

// WriteFile writes a file of the workspace, making its directories.
func (w workspace) WriteFile(_ context.Context, name string, content io.Reader, mode fs.FileMode) error {
	root, err := w.open()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name = filepath.FromSlash(name)
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return fileError(err)
		}
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fileError(err)
	}
	if _, err := io.Copy(f, content); err != nil {
		_ = f.Close()
		return core.NewError(core.CodeInternal, "the file could not be written").WithCause(err)
	}
	if err := f.Close(); err != nil {
		return core.NewError(core.CodeInternal, "the file could not be written").WithCause(err)
	}
	return nil
}

func fileError(err error) error {
	if os.IsNotExist(err) {
		return core.NewError(core.CodeNotFound, "no such file in the workspace").WithCause(err)
	}
	return core.NewError(core.CodeInvalidArgument, "the name does not lead to a file inside the workspace").WithCause(err)
}

// Snapshot is NOT_SUPPORTED.
func (b *Backend) Snapshot(context.Context, execution.SandboxID) (execution.SnapshotRef, error) {
	return execution.SnapshotRef{}, core.NewError(core.CodeNotSupported, "the local backend takes no snapshots")
}

// Inspect returns what the backend knows of a sandbox.
func (b *Backend) Inspect(_ context.Context, id execution.SandboxID) (execution.SandboxInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	if !ok {
		return execution.SandboxInfo{}, core.NewError(core.CodeNotFound, "no such sandbox")
	}
	return sb.info, nil
}

// Destroy removes the sandbox's workspace. Destroying a sandbox that is
// already gone succeeds.
func (b *Backend) Destroy(_ context.Context, id execution.SandboxID) error {
	b.mu.Lock()
	sb, ok := b.sandboxes[id]
	if ok {
		sb.info.State = execution.SandboxDestroyed
	}
	b.mu.Unlock()
	if !ok {
		return nil
	}
	if err := os.RemoveAll(sb.dir); err != nil {
		return core.NewError(core.CodeUnavailable, "the sandbox workspace could not be removed").WithCause(err)
	}
	return nil
}
