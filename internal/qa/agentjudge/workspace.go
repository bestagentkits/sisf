package agentjudge

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
)

// newWorkspace makes a fresh workspace for one judgment under root: the
// whole diff and the changed files at the candidate's commit, then every
// file and directory made read-only. Nothing of the coder's workspace is in
// it. The caller removes it with removeWorkspace.
func newWorkspace(root string, m Materials) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the QA workspace root cannot be made").WithCause(err)
	}
	dir, err := os.MkdirTemp(root, "qa-")
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the QA workspace cannot be made").WithCause(err)
	}
	if err := fill(dir, m); err != nil {
		removeWorkspace(dir)
		return "", err
	}
	if err := freeze(dir); err != nil {
		removeWorkspace(dir)
		return "", err
	}
	return dir, nil
}

// fill writes the materials into dir.
func fill(dir string, m Materials) error {
	if err := os.WriteFile(filepath.Join(dir, workspaceDiff), []byte(m.Diff), 0o600); err != nil {
		return core.NewError(core.CodeUnavailable, "the candidate diff cannot be written to the QA workspace").WithCause(err)
	}
	files := filepath.Join(dir, workspaceFiles)
	for _, f := range m.Files {
		target := filepath.Join(files, filepath.FromSlash(f.Path))
		if rel, err := filepath.Rel(files, target); err != nil || !filepath.IsLocal(rel) {
			return core.NewError(core.CodeInvalidArgument, "a candidate file has a path outside the QA workspace")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return core.NewError(core.CodeUnavailable, "the QA workspace cannot be filled").WithCause(err)
		}
		if err := os.WriteFile(target, f.Content, 0o600); err != nil {
			return core.NewError(core.CodeUnavailable, "the QA workspace cannot be filled").WithCause(err)
		}
	}
	return nil
}

// freeze makes every file and directory under dir read-only for everyone,
// the owner included, so the agent's view of the candidate cannot change.
func freeze(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, 0o400)
	})
	// Directories are frozen deepest first, after their files.
	for i := len(dirs) - 1; err == nil && i >= 0; i-- {
		err = os.Chmod(dirs[i], 0o500)
	}
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the QA workspace cannot be made read-only").WithCause(err)
	}
	return nil
}

// removeWorkspace gives the owner write access back and removes dir. It is
// best effort: a leftover workspace holds only what the agent was shown.
func removeWorkspace(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}
