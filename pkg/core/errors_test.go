package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCarriesCodeMessageAndNextActions(t *testing.T) {
	err := NewError(CodeApprovalRequired, "scope needs the Owner",
		NextAction{Action: "approvals.list", Description: "see the pending request"})
	if err.Error() != "APPROVAL_REQUIRED: scope needs the Owner" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if len(err.NextActions) != 1 || err.NextActions[0].Action != "approvals.list" {
		t.Fatalf("NextActions = %+v", err.NextActions)
	}
}

func TestCodeOfSeesThroughWrapping(t *testing.T) {
	inner := NewError(CodeStaleFencingToken, "lease was taken over")
	wrapped := fmt.Errorf("report rejected: %w", inner)
	if CodeOf(wrapped) != CodeStaleFencingToken {
		t.Errorf("CodeOf(wrapped) = %q", CodeOf(wrapped))
	}
	if !HasCode(wrapped, CodeStaleFencingToken) || HasCode(wrapped, CodeConflict) {
		t.Error("HasCode must match exactly the carried code")
	}
	if CodeOf(nil) != "" || HasCode(nil, CodeInternal) {
		t.Error("a nil error carries no code")
	}
	if CodeOf(errors.New("disk full")) != CodeInternal {
		t.Error("an untyped error must be reported as INTERNAL, never as success")
	}
}

func TestErrorKeepsItsCauseForInspection(t *testing.T) {
	cause := errors.New("connection reset")
	err := NewError(CodeUnavailable, "provider unreachable").WithCause(cause)
	if !errors.Is(err, cause) {
		t.Error("the cause must stay reachable through errors.Is")
	}
	if strings.Contains(err.Error(), "connection reset") {
		t.Error("the cause must not leak into the message shown to callers")
	}
}

func TestErrorSerializesWithTheAgentFacingFieldNames(t *testing.T) {
	raw, err := json.Marshal(NewError(CodeNotSupported, "backend cannot cancel",
		NextAction{Action: "runs.explain"}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["code"] != "NOT_SUPPORTED" || got["message"] != "backend cannot cancel" {
		t.Errorf("unexpected payload %s", raw)
	}
	if _, ok := got["next_actions"].([]any); !ok {
		t.Errorf("next_actions missing in %s", raw)
	}
}

func TestErrorCodesAreUniqueUpperSnakeCase(t *testing.T) {
	seen := map[ErrorCode]bool{}
	for _, c := range ErrorCodes() {
		if seen[c] {
			t.Errorf("code %q listed twice", c)
		}
		seen[c] = true
		if c == "" || strings.ToUpper(string(c)) != string(c) || strings.ContainsAny(string(c), " -.") {
			t.Errorf("code %q is not UPPER_SNAKE_CASE", c)
		}
		if !c.Valid() {
			t.Errorf("code %q is listed but not valid", c)
		}
	}
	for _, required := range []ErrorCode{CodeNotSupported, CodePermissionDenied, CodeConflict, CodeStaleFencingToken, CodeInternal} {
		if !seen[required] {
			t.Errorf("code %q missing from ErrorCodes()", required)
		}
	}
	if CodeNotSupported != "NOT_SUPPORTED" {
		t.Errorf("CodeNotSupported = %q", CodeNotSupported)
	}
	if ErrorCode("OK").Valid() || ErrorCode("").Valid() {
		t.Error("unknown codes must be rejected")
	}
}
