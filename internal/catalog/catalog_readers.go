package catalog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"gopkg.in/yaml.v3"
)

// Feature is one approved entry of the feature catalog.
type Feature struct {
	Name   string `json:"name" yaml:"name"`
	Scope  string `json:"scope,omitempty" yaml:"scope"`
	Status string `json:"status,omitempty" yaml:"status"`
}

// ChangelogEntry is one line of the changelog. RunID is set for the entries
// the factory recorded.
type ChangelogEntry struct {
	Date  string     `json:"date,omitempty"`
	Text  string     `json:"text"`
	RunID core.RunID `json:"run_id,omitempty"`
}

// Doc is one Markdown document of a docs tree.
type Doc struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// ListFeaturesMarkdown reads the catalog table of FEATURES.md: the first
// table with a Feature column. Cell text is returned as plain text.
func ListFeaturesMarkdown(content []byte) ([]Feature, error) {
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		header := cells(line)
		name := column(header, "feature", false)
		if name < 0 || i+1 >= len(lines) || cells(lines[i+1]) == nil {
			continue
		}
		scope, state := column(header, "scope", true), column(header, "status", true)
		features := []Feature{}
		for j := i + 2; j < len(lines); j++ {
			row := cells(lines[j])
			if row == nil {
				break
			}
			if f := (Feature{Name: cell(row, name), Scope: cell(row, scope), Status: cell(row, state)}); f.Name != "" {
				features = append(features, f)
			}
		}
		return features, nil
	}
	return nil, core.NewError(core.CodeInvalidArgument, PathFeatures+" has no table with a Feature column")
}

// cell returns the plain text of row[i], or "" when the row has no such cell.
func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return plain(row[i])
}

// ListFeaturesYAML reads the features list of features.yaml.
func ListFeaturesYAML(content []byte) ([]Feature, error) {
	var doc struct {
		Features []Feature `yaml:"features"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, PathFeaturesYAML+" cannot be read as a feature catalogue").WithCause(err)
	}
	features := make([]Feature, 0, len(doc.Features))
	for _, f := range doc.Features {
		if f = (Feature{Name: plain(f.Name), Scope: plain(f.Scope), Status: plain(f.Status)}); f.Name != "" {
			features = append(features, f)
		}
	}
	return features, nil
}

// ReadFeatures reads the feature catalog of repo at revision, from
// features.yaml when the repository has it and FEATURES.md otherwise. A
// repository with neither has an empty catalog.
func ReadFeatures(ctx context.Context, files FileReader, repo github.RepoRef, revision core.CommitSHA) ([]Feature, error) {
	for _, form := range []struct {
		path string
		list func([]byte) ([]Feature, error)
	}{{PathFeaturesYAML, ListFeaturesYAML}, {PathFeatures, ListFeaturesMarkdown}} {
		content, err := files.ReadFile(ctx, repo, revision, form.path)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			continue
		case err != nil:
			return nil, err
		}
		return form.list(content)
	}
	return []Feature{}, nil
}

// ListChangelog returns the entries of a changelog in file order, newest
// first for a changelog the factory writes, at most limit of them when
// limit is positive. Every list item is an entry; other lines are not.
func ListChangelog(content []byte, limit int) []ChangelogEntry {
	entries := []ChangelogEntry{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() && (limit <= 0 || len(entries) < limit) {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") {
			continue
		}
		entries = append(entries, changelogEntry(line[2:]))
	}
	return entries
}

// changelogEntry splits the date and the release marker off one item.
func changelogEntry(item string) ChangelogEntry {
	var e ChangelogEntry
	if at := strings.Index(item, "<!-- sisf:release "); at >= 0 {
		marker := strings.TrimSuffix(strings.TrimSpace(item[at+len("<!-- sisf:release "):]), "-->")
		if run := core.RunID(strings.TrimSpace(marker)); run.Validate() == nil {
			e.RunID = run
		}
		item = item[:at]
	}
	if date, rest, ok := strings.Cut(item, ": "); ok && len(date) == len("2006-01-02") && strings.Count(date, "-") == 2 {
		e.Date, item = date, rest
	}
	e.Text = plain(item)
	return e
}

// maxDocTitleBytes bounds how much of a document is read to find its title.
const maxDocTitleBytes = 8 << 10

// ListDocs lists the Markdown documents under root of fsys, sorted by path,
// each with its first top-level heading as title, or its file name when it
// has none.
func ListDocs(fsys fs.FS, root string) ([]Doc, error) {
	docs := []Doc{}
	err := fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(name), ".md") {
			return nil
		}
		title, err := docTitle(fsys, name)
		if err != nil {
			return err
		}
		docs = append(docs, Doc{Path: name, Title: title})
		return nil
	})
	if err != nil {
		return nil, core.NewError(core.CodeNotFound, "the documents under "+root+" cannot be listed").WithCause(err)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, nil
}

// docTitle reads the first "# " heading of one document.
func docTitle(fsys fs.FS, name string) (string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, maxDocTitleBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	for _, line := range strings.Split(string(head[:n]), "\n") {
		if strings.HasPrefix(line, "# ") {
			if title := plain(line[2:]); title != "" {
				return title, nil
			}
		}
	}
	return path.Base(name), nil
}
