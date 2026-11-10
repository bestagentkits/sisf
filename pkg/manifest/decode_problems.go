package manifest

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/internal/core"
)

// The decoder reports a rejected key or value in terms of the Go types it was
// filling. These match its two such forms so they can be said in terms of the
// file instead.
var (
	unknownKeyProblem = regexp.MustCompile(`^(line \d+: )field (.+) not found in type \S+$`)
	wrongKindProblem  = regexp.MustCompile("^(line \\d+: )cannot unmarshal !!(\\w+)(?: `(.*)`)? into .+$")
)

// yamlKinds names each kind of YAML value the way its author thinks of it.
var yamlKinds = map[string]string{
	"str":   "text",
	"int":   "an integer",
	"float": "a number",
	"bool":  "a true/false value",
	"seq":   "a list",
	"map":   "a mapping",
	"null":  "an empty value",
}

// decodeProblems describes why a document could not be decoded, one problem
// per line of the file, without the names of the types behind the manifest:
// whoever edits factory.yaml knows its keys and lines, not this package.
func decodeProblems(data []byte, err error) string {
	var typed *yaml.TypeError
	if !errors.As(err, &typed) {
		if problem := textProblem(data); problem != "" {
			return problem
		}
		// A syntax error already speaks about the file ("line 3: did not find…").
		return strings.TrimPrefix(err.Error(), "yaml: ")
	}
	problems := make([]string, len(typed.Errors))
	for i, problem := range typed.Errors {
		problems[i] = plainProblem(problem)
	}
	return strings.Join(problems, "; ")
}

// plainProblem rewrites one decoder message; a form it does not recognize is
// passed through unchanged rather than guessed at.
func plainProblem(problem string) string {
	if m := unknownKeyProblem.FindStringSubmatch(problem); m != nil {
		return m[1] + `unknown key "` + m[2] + `"`
	}
	if m := wrongKindProblem.FindStringSubmatch(problem); m != nil {
		kind, known := yamlKinds[m[2]]
		if !known {
			kind = "this value"
		}
		if m[3] != "" {
			kind += " (" + m[3] + ")"
		}
		return m[1] + kind + " does not fit here"
	}
	return problem
}

// textUnmarshaler is the interface of the manifest's own text forms, such as
// a duration.
var textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

// textProblem finds the first value that one of the manifest's text forms
// refuses and reports it with its line. The decoder passes such a refusal on
// without saying where it happened, so the document is read a second time
// only to locate it. It returns "" when no such value is found.
func textProblem(data []byte) string {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return ""
	}
	node, err := refusedText(document.Content[0], reflect.TypeOf(Manifest{}))
	if node == nil {
		return ""
	}
	reason := err.Error()
	var typed *core.Error
	if errors.As(err, &typed) {
		reason = typed.Message
	}
	return fmt.Sprintf("line %d: %q: %s", node.Line, node.Value, reason)
}

// refusedText walks node in step with the type it is decoded into and
// returns the first scalar that the text form of its type refuses.
func refusedText(node *yaml.Node, t reflect.Type) (*yaml.Node, error) {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(textUnmarshaler) {
		if node.Kind != yaml.ScalarNode {
			return nil, nil
		}
		value := reflect.New(t).Interface().(encoding.TextUnmarshaler)
		if err := value.UnmarshalText([]byte(node.Value)); err != nil {
			return node, err
		}
		return nil, nil
	}
	switch {
	case t.Kind() == reflect.Struct && node.Kind == yaml.MappingNode:
		fields := map[string]reflect.Type{}
		for i := range t.NumField() {
			key, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			fields[key] = t.Field(i).Type
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if fieldType, known := fields[node.Content[i].Value]; known {
				if refused, err := refusedText(node.Content[i+1], fieldType); refused != nil {
					return refused, err
				}
			}
		}
	case t.Kind() == reflect.Map && node.Kind == yaml.MappingNode:
		for i := 1; i < len(node.Content); i += 2 {
			if refused, err := refusedText(node.Content[i], t.Elem()); refused != nil {
				return refused, err
			}
		}
	case t.Kind() == reflect.Slice && node.Kind == yaml.SequenceNode:
		for _, item := range node.Content {
			if refused, err := refusedText(item, t.Elem()); refused != nil {
				return refused, err
			}
		}
	}
	return nil, nil
}
