package manifest

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
)

// SchemaID is the address the published schema of factory.yaml is served
// from, so an editor can fetch it and flag mistakes while the file is typed.
const SchemaID = "https://raw.githubusercontent.com/bestagentkits/sisf/main/schemas/factory.schema.json"

// Schema returns the JSON Schema (draft 2020-12) of factory.yaml as a fresh
// document tree.
//
// The shape is derived from the Go types of this package: a key is in the
// schema exactly when it is a field of Manifest, and a field without
// omitempty is required. What a Go type cannot say (allowed values, patterns,
// ranges, which map entries must exist) comes from the tables in
// schema_rules.go. The schema describes a well-formed manifest; whether the
// deployment it declares is safe is the conformance checker's question.
func Schema() map[string]any {
	b := &schemaBuilder{scalars: scalarSchemas(), defs: map[string]any{}, applied: map[string]bool{}}
	rootName := b.define(reflect.TypeOf(Manifest{}))
	root := b.defs[rootName].(map[string]any)
	delete(b.defs, rootName)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$id"] = SchemaID
	root["title"] = "SISF factory manifest"
	root["description"] = "Desired state of one factory deployment: one factory_id, three zones, nine runtime units and the trusted release supervisor, sharing one release manifest and one lifecycle."
	root["$defs"] = b.defs
	return root
}

// SchemaJSON renders Schema in the exact form published in
// schemas/factory.schema.json: indented, keys sorted, one trailing newline.
func SchemaJSON() ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	// Patterns are read by people and by editors; keep them unescaped.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Schema()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// schemaBuilder turns Go types into schema nodes. Struct types become named
// definitions so each is described once and referenced wherever it is used.
type schemaBuilder struct {
	scalars map[reflect.Type]map[string]any
	defs    map[string]any
	// applied records which entries of the rule tables matched a real type or
	// field, so a rule left behind by a renamed field is detectable.
	applied map[string]bool
}

// of returns the schema of a value of type t.
func (b *schemaBuilder) of(t reflect.Type) map[string]any {
	if scalar, ok := b.scalars[t]; ok {
		return maps.Clone(scalar)
	}
	switch t.Kind() {
	case reflect.Pointer:
		return b.of(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": b.of(t.Elem())}
	case reflect.Map:
		keys, closed := b.of(t.Key())["enum"].([]string)
		if !closed {
			return map[string]any{"type": "object", "additionalProperties": b.of(t.Elem())}
		}
		// A map keyed by a closed set of names lists each name as a property
		// and accepts no other key, which also lets an editor suggest them.
		properties := map[string]any{}
		for _, key := range keys {
			properties[key] = b.of(t.Elem())
		}
		return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	case reflect.Struct:
		return map[string]any{"$ref": "#/$defs/" + b.define(t)}
	}
	// Free-form values (extension and adapter settings) accept anything.
	return map[string]any{}
}

// define registers the object schema of struct type t and returns its name.
func (b *schemaBuilder) define(t reflect.Type) string {
	name := t.Name()
	if _, seen := b.defs[name]; seen {
		return name
	}
	def := map[string]any{"type": "object", "additionalProperties": false}
	b.defs[name] = def // registered before the fields so a cycle terminates

	properties := map[string]any{}
	required := []string{}
	for i := range t.NumField() {
		field := t.Field(i)
		key, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || key == "" || key == "-" {
			continue
		}
		property := b.of(field.Type)
		b.merge(property, fieldRules, name+"."+key)
		properties[key] = property
		if !strings.Contains(options, "omitempty") {
			required = append(required, key)
		}
	}
	def["properties"] = properties
	if len(required) > 0 {
		def["required"] = required
	}
	b.merge(def, typeRules, name)
	return name
}

// merge copies the rule stored under key, if any, over node.
func (b *schemaBuilder) merge(node map[string]any, rules map[string]map[string]any, key string) {
	if rule, ok := rules[key]; ok {
		maps.Copy(node, rule)
		b.applied[key] = true
	}
}

// staleSchemaRules lists rule-table entries that no longer match a type or a
// field of the manifest. It is empty while the tables and the types agree.
func staleSchemaRules() []string {
	b := &schemaBuilder{scalars: scalarSchemas(), defs: map[string]any{}, applied: map[string]bool{}}
	b.define(reflect.TypeOf(Manifest{}))
	var stale []string
	for _, rules := range []map[string]map[string]any{fieldRules, typeRules} {
		for key := range rules {
			if !b.applied[key] {
				stale = append(stale, key)
			}
		}
	}
	return stale
}
