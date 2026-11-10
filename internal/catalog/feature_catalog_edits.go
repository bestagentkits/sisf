package catalog

import (
	"bytes"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"gopkg.in/yaml.v3"
)

// unapproved refuses a change to a feature the catalog does not list.
func unapproved(feature string) *core.Error {
	return core.NewError(core.CodeApprovalRequired,
		"the catalog does not list the feature "+plain(feature)+"; a new feature needs the Owner's approval before it is listed")
}

// SetFeatureStatusMarkdown sets the status of one listed feature in the
// catalog table of FEATURES.md: the first table with a Feature and a Status
// column. A feature the table does not list is APPROVAL_REQUIRED. Every
// other line is kept as it is.
func SetFeatureStatusMarkdown(content []byte, feature, status string) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		header := cells(line)
		name, state := column(header, "feature", false), column(header, "status", true)
		if name < 0 || i+1 >= len(lines) || cells(lines[i+1]) == nil {
			continue
		}
		if state < 0 {
			return nil, core.NewError(core.CodeInvalidArgument, PathFeatures+" has no status column to update")
		}
		for j := i + 2; j < len(lines); j++ {
			row := cells(lines[j])
			if row == nil {
				break
			}
			if name < len(row) && featureKey(row[name]) == featureKey(feature) {
				for len(row) <= state {
					row = append(row, "")
				}
				row[state] = plain(status)
				lines[j] = "| " + strings.Join(row, " | ") + " |"
				return []byte(strings.Join(lines, "\n")), nil
			}
		}
		return nil, unapproved(feature)
	}
	return nil, core.NewError(core.CodeInvalidArgument, PathFeatures+" has no table with a Feature column")
}

// cells splits a Markdown table row into trimmed cells, or nil.
func cells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// column finds a header cell equal to name, or containing it when loose.
func column(header []string, name string, loose bool) int {
	for i, cell := range header {
		cell = strings.ToLower(cell)
		if cell == name || (loose && strings.Contains(cell, name)) {
			return i
		}
	}
	return -1
}

// SetFeatureStatusYAML sets the status of one listed feature in
// features.yaml, a "features" list of entries with a name, a scope and a
// status. Comments and the order of keys are kept. A feature the list does
// not hold is APPROVAL_REQUIRED.
func SetFeatureStatusYAML(content []byte, feature, status string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, core.NewError(core.CodeInvalidArgument, PathFeaturesYAML+" cannot be read as a feature catalogue")
	}
	list := valueOf(doc.Content[0], "features")
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil, core.NewError(core.CodeInvalidArgument, PathFeaturesYAML+" has no features list")
	}
	for _, item := range list.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if name := valueOf(item, "name"); name == nil || featureKey(name.Value) != featureKey(feature) {
			continue
		}
		if node := valueOf(item, "status"); node != nil {
			node.Value, node.Tag, node.Style = plain(status), "!!str", 0
		} else {
			item.Content = append(item.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "status"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: plain(status)})
		}
		var out bytes.Buffer
		encoder := yaml.NewEncoder(&out)
		encoder.SetIndent(2)
		if err := encoder.Encode(&doc); err != nil {
			return nil, core.NewError(core.CodeInternal, PathFeaturesYAML+" cannot be written").WithCause(err)
		}
		if err := encoder.Close(); err != nil {
			return nil, core.NewError(core.CodeInternal, PathFeaturesYAML+" cannot be written").WithCause(err)
		}
		return out.Bytes(), nil
	}
	return nil, unapproved(feature)
}

// valueOf returns the value node of key in a mapping node, or nil.
func valueOf(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
