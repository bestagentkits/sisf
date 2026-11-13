package managedapp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxSize is the largest Spec accepted, in bytes.
const MaxSize = 256 << 10

// Parse decodes one Spec strictly: an unknown key, a wrong type or a second
// document is an error, so a typo never silently drops a setting. It then
// validates the Spec and returns every problem it has.
func Parse(data []byte) (*Spec, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", FileName, MaxSize)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s is empty", FileName)
		}
		return nil, fmt.Errorf("%s cannot be decoded: %w", FileName, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s holds more than one document", FileName)
	}
	if problems := s.Validate(); len(problems) > 0 {
		return nil, &InvalidError{Problems: problems}
	}
	return &s, nil
}

// Load reads and parses the Spec at path.
func Load(path string) (*Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Marshal encodes s as YAML, with a header saying how it was made.
func Marshal(s *Spec, header string) ([]byte, error) {
	var buf bytes.Buffer
	for _, line := range strings.Split(strings.TrimSpace(header), "\n") {
		if line != "" {
			buf.WriteString("# " + line + "\n")
		}
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// InvalidError lists everything wrong with a Spec.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string {
	return FileName + " is invalid: " + strings.Join(e.Problems, "; ")
}
