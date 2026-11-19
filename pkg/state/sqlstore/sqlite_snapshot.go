package sqlstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
)

// SnapshotSQLite writes a consistent copy of the whole SQLite database,
// schema and rows, to a new file at path, readable by its owner only. The
// directory of path is created readable by its owner only if it does not
// exist. The copy is a database of its own: put it in place of the store's
// file, with the factory stopped, to return to it. A file at path is never
// overwritten. Only a store opened for the control zone takes a snapshot,
// and only of a SQLite database; a PostgreSQL store is dumped with its own
// tools.
func (s *Store) SnapshotSQLite(ctx context.Context, path string) error {
	if s.zone != core.ZoneControl {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not snapshot the state store", string(s.zone)))
	}
	if s.dialect != DialectSQLite {
		return invalid("only a SQLite store is snapshot by copying its database")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return core.NewError(core.CodeUnavailable, "cannot create the directory of the snapshot").WithCause(err)
	}
	if _, err := os.Lstat(path); err == nil {
		return core.NewError(core.CodeConflict, fmt.Sprintf("the snapshot %s exists already", path))
	}
	// VACUUM INTO reads the database in one transaction, so the copy is
	// consistent while other connections write.
	if _, err := s.write.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return s.dialect.failure("snapshot the state store", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return core.NewError(core.CodeUnavailable, "cannot restrict the snapshot to its owner").WithCause(err)
	}
	return nil
}
