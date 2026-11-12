package actions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxInputBytes bounds one action input.
const maxInputBytes = 1 << 20

// canonicalInput checks that the input is one JSON object and returns it in
// canonical form: keys sorted, no insignificant space, numbers as written.
// The same parameters therefore always produce the same bytes, which is what
// an approval is bound to. An empty input is the empty object.
func canonicalInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > maxInputBytes {
		return nil, core.NewError(core.CodeInvalidArgument, "the action input is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the action input must be one JSON object")
	}
	if dec.More() {
		return nil, core.NewError(core.CodeInvalidArgument, "the action input must be exactly one JSON object")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the action input cannot be encoded").WithCause(err)
	}
	return out, nil
}

// subjectDigest binds an approval to the factory, the action and its exact
// canonical input. Changing any parameter changes the digest, so an approval
// given for one call never covers another.
func subjectDigest(factory core.FactoryID, action string, canonical json.RawMessage) (core.Digest, error) {
	payload := core.NewPayloadBuilder("sisf.action", 1).
		Text("factory_id", string(factory)).
		Text("action", action).
		Text("input", string(canonical)).
		Bytes()
	sum := sha256.Sum256(payload)
	digest := core.Digest("sha256:" + hex.EncodeToString(sum[:]))
	if err := digest.Validate(); err != nil {
		return "", core.NewError(core.CodeInternal, "the subject digest is malformed").WithCause(err)
	}
	return digest, nil
}

// requestID names the nth approval request filed for one exact change, so
// planning the same change twice finds the request already filed.
func requestID(digest core.Digest, n int) string {
	_, hexPart, _ := bytes.Cut([]byte(digest), []byte(":"))
	return fmt.Sprintf("act-%s-%d", hexPart[:32], n)
}
