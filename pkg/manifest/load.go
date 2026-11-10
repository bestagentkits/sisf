package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/internal/core"
)

// MaxSize is the largest manifest file accepted, in bytes. A manifest is a
// short declaration; anything larger is refused before it is parsed.
const MaxSize = 1 << 20

// Parse decodes one YAML document into a Manifest. It is strict about shape:
// an unknown key, a value of the wrong type, a repeated key or a second
// document is an error, so a typo never silently drops a setting. Parse does
// not judge the values; Validate and the conformance checker do.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxSize {
		return nil, invalidf("manifest is larger than %d bytes", MaxSize)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalidf("manifest is empty")
		}
		return nil, invalidf("manifest cannot be decoded: %s", decodeProblems(data, err)).WithCause(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidf("manifest must hold exactly one YAML document")
	}
	return &m, nil
}

// ParseFile reads and parses the manifest at path without validating its
// values. A missing file is NOT_FOUND; an unreadable one is UNAVAILABLE.
func ParseFile(path string) (*Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, core.NewError(core.CodeNotFound, fmt.Sprintf("manifest %s does not exist", path)).WithCause(err)
		}
		return nil, core.NewError(core.CodeUnavailable, fmt.Sprintf("manifest %s cannot be opened", path)).WithCause(err)
	}
	// The file was only read, so a failed close cannot lose data.
	defer func() { _ = file.Close() }()

	// One byte beyond the limit is enough to tell "too large" from "at the limit".
	data, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, fmt.Sprintf("manifest %s cannot be read", path)).WithCause(err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, inFile(path, err)
	}
	return m, nil
}

// Load reads the manifest at path and returns it only when it matches the
// published schema. Use the conformance checker to also verify the
// deployment rules a schema cannot express.
func Load(path string) (*Manifest, error) {
	m, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	violations := Validate(m)
	if len(violations) == 0 {
		return m, nil
	}
	lines := make([]string, 0, len(violations))
	details := make(map[string]string, len(violations))
	for _, v := range violations {
		lines = append(lines, v.String())
		details[v.Path] = v.Message
	}
	sort.Strings(lines)
	failure := invalidf("manifest %s does not match the schema: %s", path, strings.Join(lines, "; "))
	failure.Details = details
	return nil, failure
}

// invalidf builds the INVALID_ARGUMENT error used for a manifest that cannot
// be accepted as written.
func invalidf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

// inFile names the file in a parse error while keeping its code and cause.
func inFile(path string, err error) error {
	var typed *core.Error
	if !errors.As(err, &typed) {
		return err
	}
	named := core.NewError(typed.Code, path+": "+typed.Message, typed.NextActions...)
	named.Details = typed.Details
	return named.WithCause(err)
}
