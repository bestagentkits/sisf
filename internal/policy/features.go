package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Feature is one entry of the catalogue of functionality a person approved.
type Feature struct {
	Name string `yaml:"name"`
	// Scope says what was approved, usually a reference to the brief.
	Scope string `yaml:"scope"`
	// Status says whether the feature is built. Approved is not built.
	Status string `yaml:"status"`
}

// Catalog is the approved feature catalogue. Work on something it does not
// list is a new feature and needs the Owner.
type Catalog struct {
	Features []Feature
}

// Has reports whether the catalogue lists a feature of this name. Case and
// spacing do not matter; anything else does.
func (c Catalog) Has(name string) bool {
	wanted := featureKey(name)
	if wanted == "" {
		return false
	}
	for _, f := range c.Features {
		if featureKey(f.Name) == wanted {
			return true
		}
	}
	return false
}

// featureKey is the form in which feature names are compared.
func featureKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// fingerprint renders the catalogue as one comparable value, so the two
// forms of the catalogue can be checked against each other.
func (c Catalog) fingerprint() string {
	var b strings.Builder
	for _, f := range c.Features {
		fmt.Fprintf(&b, "%q %q %q\n", featureKey(f.Name), strings.Join(strings.Fields(f.Scope), " "), strings.Join(strings.Fields(f.Status), " "))
	}
	return string(digestOf([]byte(b.String())))
}

// check rejects a catalogue with an unnamed or a repeated feature. A feature
// listed twice could carry two scopes, and nothing says which one counts.
func (c Catalog) check(path string) error {
	seen := map[string]bool{}
	for _, f := range c.Features {
		key := featureKey(f.Name)
		switch {
		case key == "":
			return hold(reasonInvalid, "%s lists a feature without a name", path)
		case seen[key]:
			return hold(reasonConflict, "%s lists the feature %q more than once", path, f.Name)
		}
		seen[key] = true
	}
	return nil
}

// parseFeaturesYAML reads features.yaml: a "features" list of entries with a
// name, a scope and a status. Unknown keys are refused, so a misspelt key
// never silently drops part of an entry.
func parseFeaturesYAML(content []byte) (Catalog, error) {
	var file struct {
		Features []Feature `yaml:"features"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Catalog{}, hold(reasonInvalid, "%s cannot be read as a feature catalogue", PathFeaturesYAML).WithCause(err)
	}
	catalog := Catalog{Features: file.Features}
	return catalog, catalog.check(PathFeaturesYAML)
}

// parseFeaturesMarkdown reads the catalogue table of FEATURES.md: the first
// table whose header has a "Feature" column. The "scope" and "status"
// columns are found by their header and may be absent.
func parseFeaturesMarkdown(content []byte) (Catalog, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	for i, line := range lines {
		header := tableCells(line)
		name := columnOf(header, func(cell string) bool { return cell == "feature" })
		if name < 0 || i+1 >= len(lines) || !isTableRule(lines[i+1]) {
			continue
		}
		scope := columnOf(header, func(cell string) bool { return strings.Contains(cell, "scope") })
		status := columnOf(header, func(cell string) bool { return strings.Contains(cell, "status") })
		var catalog Catalog
		for _, row := range lines[i+2:] {
			cells := tableCells(row)
			if cells == nil {
				break
			}
			catalog.Features = append(catalog.Features, Feature{
				Name: cellAt(cells, name), Scope: cellAt(cells, scope), Status: cellAt(cells, status),
			})
		}
		return catalog, catalog.check(PathFeatures)
	}
	return Catalog{}, hold(reasonInvalid, "%s has no table with a Feature column", PathFeatures)
}

// tableCells splits one Markdown table row into its trimmed cells, or
// returns nil when the line is not a table row.
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	// An escaped pipe belongs to the cell text, not to the table.
	const escaped = "\x00"
	parts := strings.Split(strings.ReplaceAll(line, `\|`, escaped), "|")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(strings.ReplaceAll(part, escaped, "|"))
	}
	return parts
}

// isTableRule reports whether the line is the rule under a table header,
// such as "|---|:--:|".
func isTableRule(line string) bool {
	cells := tableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if cell == "" || strings.Trim(cell, "-:") != "" {
			return false
		}
	}
	return true
}

// columnOf returns the index of the first header cell that matches, or -1.
func columnOf(header []string, match func(cell string) bool) int {
	for i, cell := range header {
		if match(strings.ToLower(cell)) {
			return i
		}
	}
	return -1
}

// cellAt returns the cell at index, or "" when the row is shorter or the
// column does not exist.
func cellAt(cells []string, index int) string {
	if index < 0 || index >= len(cells) {
		return ""
	}
	return cells[index]
}
