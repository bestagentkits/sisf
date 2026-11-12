package core

import (
	"bytes"
	"strconv"
	"time"
)

// payloadMagic opens every signing payload, so signed bytes can never be
// mistaken for anything else a key might be asked to sign.
const payloadMagic = "sisf-signed-record"

// PayloadBuilder writes the canonical bytes a signature covers. Each signed
// record type has one SigningPayload method built on it, and signers and
// verifiers use only those bytes, never a serialization that can vary.
//
// The encoding is deterministic and unambiguous. It opens with a fixed line,
// the domain of the record type and the version of its field list; a
// signature for one type or version therefore never verifies for another.
// Fields follow in the order they are written, one per line as
// name=<length>:<value>. The length prefix stops a value from spilling into
// the next field. A record that gains, loses or reorders a bound field must
// raise its version.
type PayloadBuilder struct {
	buf bytes.Buffer
}

// NewPayloadBuilder starts the payload of one record type. The domain names
// the type, for example "sisf.qa_receipt", and the version its field list.
func NewPayloadBuilder(domain string, version int) *PayloadBuilder {
	b := &PayloadBuilder{}
	b.buf.WriteString(payloadMagic + "\n")
	return b.Text("domain", domain).Int("version", int64(version))
}

// Text appends a text field.
func (b *PayloadBuilder) Text(name, value string) *PayloadBuilder {
	b.buf.WriteString(name)
	b.buf.WriteByte('=')
	b.buf.WriteString(strconv.Itoa(len(value)))
	b.buf.WriteByte(':')
	b.buf.WriteString(value)
	b.buf.WriteByte('\n')
	return b
}

// Int appends a whole number in decimal.
func (b *PayloadBuilder) Int(name string, value int64) *PayloadBuilder {
	return b.Text(name, strconv.FormatInt(value, 10))
}

// Bool appends "true" or "false".
func (b *PayloadBuilder) Bool(name string, value bool) *PayloadBuilder {
	return b.Text(name, strconv.FormatBool(value))
}

// Time appends an instant in UTC with nanosecond precision, so the same
// instant encodes the same whatever zone it is expressed in.
func (b *PayloadBuilder) Time(name string, value time.Time) *PayloadBuilder {
	return b.Text(name, value.UTC().Format(time.RFC3339Nano))
}

// Bytes returns the payload written so far. The caller owns the result.
func (b *PayloadBuilder) Bytes() []byte { return bytes.Clone(b.buf.Bytes()) }
