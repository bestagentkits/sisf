package core

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestASigningPayloadStartsWithItsDomainAndVersion(t *testing.T) {
	t.Parallel()
	payload := string(NewPayloadBuilder("sisf.example", 3).Text("subject", "x").Bytes())
	want := "sisf-signed-record\ndomain=12:sisf.example\nversion=1:3\nsubject=1:x\n"
	if payload != want {
		t.Fatalf("payload = %q, want %q", payload, want)
	}
}

func TestTheSameFieldsAlwaysEncodeToTheSameBytes(t *testing.T) {
	t.Parallel()
	build := func() []byte {
		return NewPayloadBuilder("sisf.example", 1).
			Text("name", "tests_contract").
			Int("score", 90).
			Bool("passed", true).
			Time("issued_at", time.Date(2026, 11, 10, 7, 0, 0, 5, time.UTC)).
			Bytes()
	}
	if !bytes.Equal(build(), build()) {
		t.Fatal("two encodings of the same fields differ")
	}
}

func TestAnInstantEncodesTheSameInEveryTimeZone(t *testing.T) {
	t.Parallel()
	utc := time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)
	saigon := utc.In(time.FixedZone("ICT", 7*60*60))
	encode := func(at time.Time) []byte { return NewPayloadBuilder("sisf.example", 1).Time("at", at).Bytes() }
	if !bytes.Equal(encode(utc), encode(saigon)) {
		t.Fatalf("one instant encoded differently by zone: %q and %q", encode(utc), encode(saigon))
	}
	if bytes.Equal(encode(utc), encode(utc.Add(time.Nanosecond))) {
		t.Fatal("two instants a nanosecond apart share an encoding")
	}
}

func TestPayloadsOfDifferentDomainsOrVersionsNeverCollide(t *testing.T) {
	t.Parallel()
	encode := func(domain string, version int) []byte {
		return NewPayloadBuilder(domain, version).Text("subject", "x").Bytes()
	}
	base := encode("sisf.approval", 1)
	if bytes.Equal(base, encode("sisf.qa_receipt", 1)) {
		t.Error("a payload signed for one record type is valid for another")
	}
	if bytes.Equal(base, encode("sisf.approval", 2)) {
		t.Error("a payload signed under one version is valid under another")
	}
}

func TestAValueCannotSpillIntoTheNextField(t *testing.T) {
	t.Parallel()
	// Without a length prefix both of these would read as a=x, b=y.
	honest := NewPayloadBuilder("sisf.example", 1).Text("a", "x").Text("b", "y").Bytes()
	forged := NewPayloadBuilder("sisf.example", 1).Text("a", "x\nb=1:y").Text("b", "").Bytes()
	if bytes.Equal(honest, forged) {
		t.Fatal("a value containing a field separator forged the following field")
	}
	shifted := NewPayloadBuilder("sisf.example", 1).Text("a", "xy").Text("b", "").Bytes()
	if bytes.Equal(honest, shifted) {
		t.Fatal("moving a character between adjacent fields kept the payload")
	}
}

func TestEveryFieldKindIsWrittenUnderItsName(t *testing.T) {
	t.Parallel()
	payload := string(NewPayloadBuilder("sisf.example", 1).
		Int("attempt", -2).
		Bool("passed", false).
		Time("at", time.Time{}).
		Bytes())
	for _, line := range []string{"attempt=2:-2\n", "passed=5:false\n", "at=20:0001-01-01T00:00:00Z\n"} {
		if !strings.Contains(payload, line) {
			t.Errorf("payload %q lacks %q", payload, line)
		}
	}
}
