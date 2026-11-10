package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// Violation is one place where a manifest departs from the published schema.
type Violation struct {
	// Path locates the offending value in manifest keys, for example
	// "units.runner.mounts[0].path". It is empty when the whole document is
	// at fault.
	Path string `json:"path"`
	// Message says what is wrong with the value at Path. It never repeats a
	// text value that was refused for its form, because that text may be a
	// secret.
	Message string `json:"message"`
}

// String renders "path: message".
func (v Violation) String() string {
	if v.Path == "" {
		return v.Message
	}
	return v.Path + ": " + v.Message
}

// compiledSchema compiles the schema once; every validation reuses it.
var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	raw, err := SchemaJSON()
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(SchemaID, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(SchemaID)
})

// Validate checks m against the published schema, and against the few value
// rules a schema cannot state, and returns every violation, sorted by path.
// The manifest is validated in its JSON form, so a manifest built in code and
// one read from a file are judged alike. An empty result means the manifest
// is well formed, not that the deployment it declares is conformant.
func Validate(m *Manifest) []Violation {
	found := schemaViolations(m)
	if m == nil || (len(found) == 1 && found[0].Path == "") {
		return found
	}
	// A value the schema already refused is reported once, by the schema.
	for _, extra := range decisionViolations(m) {
		if !slices.ContainsFunc(found, func(v Violation) bool { return within(v.Path, extra.Path) }) {
			found = append(found, extra)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Path != found[j].Path {
			return found[i].Path < found[j].Path
		}
		return found[i].Message < found[j].Message
	})
	return found
}

// within reports whether path is at or below parent.
func within(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+".") || strings.HasPrefix(path, parent+"[")
}

// schemaViolations checks m against the published schema only.
func schemaViolations(m *Manifest) []Violation {
	if m == nil {
		return []Violation{{Message: "no manifest was given"}}
	}
	schema, err := compiledSchema()
	if err != nil {
		return []Violation{{Message: "the manifest schema does not compile: " + err.Error()}}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return []Violation{{Message: "a free-form setting cannot be written as JSON (extensions and adapter config need text keys): " + err.Error()}}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []Violation{{Message: "the manifest cannot be read back as JSON: " + err.Error()}}
	}
	err = schema.Validate(instance)
	if err == nil {
		return nil
	}
	var failed *jsonschema.ValidationError
	if !errors.As(err, &failed) {
		return []Violation{{Message: err.Error()}}
	}
	var found []Violation
	collectViolations(failed, &found)
	return found
}

// collectViolations walks the error tree and records its leaves, the errors
// that name a concrete value. Inner nodes only say "something below failed".
func collectViolations(failed *jsonschema.ValidationError, found *[]Violation) {
	if len(failed.Causes) > 0 {
		for _, cause := range failed.Causes {
			collectViolations(cause, found)
		}
		return
	}
	at := failed.InstanceLocation
	switch k := failed.ErrorKind.(type) {
	case *kind.Required:
		// A missing key is reported at the key, where it has to be added.
		for _, missing := range k.Missing {
			*found = append(*found, Violation{Path: dotted(at, missing), Message: "is required"})
		}
	case *kind.AdditionalProperties:
		for _, extra := range k.Properties {
			*found = append(*found, Violation{Path: dotted(at, extra), Message: "is not an allowed key here"})
		}
	case *kind.Pattern:
		// The refused text may be a secret written where a reference belongs,
		// so only the expected form is reported.
		*found = append(*found, Violation{Path: dotted(at), Message: "does not match pattern '" + k.Want + "' (the value is not shown)"})
	default:
		*found = append(*found, Violation{Path: dotted(at), Message: failed.BasicOutput().Error.String()})
	}
}

// dotted joins location tokens into a manifest path: keys are separated by
// dots and list positions are written in brackets.
func dotted(tokens []string, more ...string) string {
	var path strings.Builder
	for _, token := range append(append([]string{}, tokens...), more...) {
		if _, err := strconv.Atoi(token); err == nil && path.Len() > 0 {
			path.WriteString("[" + token + "]")
			continue
		}
		if path.Len() > 0 {
			path.WriteByte('.')
		}
		path.WriteString(token)
	}
	return path.String()
}
