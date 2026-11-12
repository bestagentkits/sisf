package install

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// SourceRecord is the file in which init records the source checkout the
// install is built from, so a later `sisf up`, and the service unit that
// runs it from another directory, build from the same checkout.
func (l Layout) SourceRecord() string { return filepath.Join(l.Dir, "source.json") }

// sourceRecord is the content of the source record.
type sourceRecord struct {
	SourceDir string `json:"source_dir"`
}

// writeSourceRecord records the source checkout of a new install. It holds
// a path and nothing secret.
func writeSourceRecord(l Layout, source string) error {
	data, err := json.MarshalIndent(sourceRecord{SourceDir: source}, "", "  ")
	if err != nil {
		return invalidf("the source record cannot be encoded")
	}
	if err := os.WriteFile(l.SourceRecord(), append(data, '\n'), 0o644); err != nil {
		return unavailablef(err, "cannot write %s", l.SourceRecord())
	}
	return nil
}

// RecordedSource returns the source checkout init recorded for the install
// in dir, or "" when it recorded none, as an install made before the record
// existed.
func RecordedSource(dir string) (string, error) {
	l := Layout{Dir: dir}
	data, err := os.ReadFile(l.SourceRecord())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", unavailablef(err, "cannot read %s", l.SourceRecord())
	}
	var rec sourceRecord
	if err := json.Unmarshal(data, &rec); err != nil || !filepath.IsAbs(rec.SourceDir) {
		return "", invalidf("%s is damaged: it must name the absolute path of the source checkout", l.SourceRecord())
	}
	return rec.SourceDir, nil
}
