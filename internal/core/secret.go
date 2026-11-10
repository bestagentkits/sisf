package core

import (
	"fmt"
	"io"
)

const redacted = "[REDACTED]"

// SecretValue holds secret bytes and refuses to print them. Formatting,
// logging and encoding all yield a fixed marker, so a secret that ends up in
// a log line, an error or a JSON document is not disclosed. The content is
// reachable only through Reveal.
type SecretValue struct {
	// box is a pointer so that reflection-based printers, which bypass the
	// methods below for unexported struct fields, show an address and not
	// the bytes.
	box *secretBytes
}

type secretBytes struct{ b []byte }

// NewSecretValue wraps a copy of b.
func NewSecretValue(b []byte) SecretValue {
	return SecretValue{box: &secretBytes{b: append([]byte(nil), b...)}}
}

// Reveal returns a copy of the secret bytes, or nil for the zero value. Call
// it only at the point where the secret is handed to its consumer.
func (s SecretValue) Reveal() []byte {
	if s.box == nil {
		return nil
	}
	return append([]byte(nil), s.box.b...)
}

// IsZero reports whether no secret is held.
func (s SecretValue) IsZero() bool { return s.box == nil || len(s.box.b) == 0 }

// String returns the redaction marker.
func (s SecretValue) String() string { return redacted }

// Format writes the redaction marker for every verb and flag.
func (s SecretValue) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalText encodes the redaction marker; JSON and text encoders use it.
func (s SecretValue) MarshalText() ([]byte, error) { return []byte(redacted), nil }
