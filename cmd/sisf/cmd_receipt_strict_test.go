package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The root key is pinned by a fingerprint the auditor got from the Owner
// apart from the bundle: a key whose fingerprint differs is refused. The
// report and the JSON output name the root key and the assurance level, and
// say that a holder of the root key could have signed anything; a root key
// kept off the factory host is stated only when the auditor says so.
func TestReceiptVerifyPinsTheRootKeyAndStatesTheAssurance(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	rootID := f.delegation.Delegation.RootKeyID
	status, stdout, stderr := runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--root-key-id", rootID, f.bundle)...)
	if status != exitOK {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	for _, want := range []string{rootID, string(delegation.AssuranceLower), "whoever holds the root key"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
	status, _, stderr = runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--root-key-id", "ed25519:"+strings.Repeat("0", 64), f.bundle)...)
	if status != exitFailure || !strings.Contains(stderr, "fingerprint") {
		t.Errorf("a root key of another fingerprint: exit %d, %s", status, stderr)
	}
	status, stdout, stderr = runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--root-key-id", rootID, "--root-offline", "--json", f.bundle)...)
	var out struct {
		RootKeyID string `json:"root_key_id"`
		Assurance string `json:"assurance"`
		Pinned    bool   `json:"root_key_pinned"`
	}
	if status != exitOK || json.Unmarshal([]byte(stdout), &out) != nil || out.RootKeyID != rootID ||
		out.Assurance != string(delegation.AssuranceRootOffline) || !out.Pinned {
		t.Fatalf("--json: exit %d, %+v, %s", status, out, stderr)
	}
}

// Exit status 0 means the receipt is authentic and its hard gates passed. An
// authentic receipt whose gates did not pass exits 3 and is not called
// verified, unless --allow-failed asks to inspect it. Expiry is always
// reported and refused, with exit 4, only under --require-unexpired.
func TestReceiptVerifyExitStatusSaysWhetherTheGatesPassed(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	failed := f.receipt
	failed.Checks = []core.QACheck{{Name: "tests", Kind: core.CheckHardGate, Passed: false},
		{Name: "correctness", Kind: core.CheckRubric, Passed: true, Points: 28, MaxPoints: 30}}
	failedFile := f.resign(t, "failed.json", failed)
	expired := f.receipt
	expired.ExpiresAt = expired.IssuedAt.Add(time.Minute)
	expiredFile := f.resign(t, "expired.json", expired)
	for name, c := range map[string]struct {
		extra  []string
		file   string
		status int
		says   string
	}{
		"gates failed":                   {nil, failedFile, exitGatesFailed, "hard gates did NOT pass"},
		"gates failed, --allow-failed":   {[]string{"--allow-failed"}, failedFile, exitOK, "hard gates did NOT pass"},
		"expired, reported":              {nil, expiredFile, exitOK, "expired"},
		"expired, --require-unexpired":   {[]string{"--require-unexpired"}, expiredFile, exitExpired, "expired"},
		"unexpired, --require-unexpired": {[]string{"--require-unexpired"}, f.bundle, exitOK, "still valid"},
	} {
		args := append(claimArgs(f, f.rootKey, "run-1"), c.extra...)
		status, stdout, stderr := runWithOutput(t, append(args, c.file)...)
		if status != c.status {
			t.Errorf("%s: exit %d, want %d: %s", name, status, c.status, stderr)
		}
		if !strings.Contains(stdout+stderr, c.says) {
			t.Errorf("%s: the output does not say %q:\n%s%s", name, c.says, stdout, stderr)
		}
		if status != exitOK && strings.Contains(stdout, "QA receipt verified") {
			t.Errorf("%s: a receipt that did not pass was reported verified:\n%s", name, stdout)
		}
	}
}

// The auditor binds the receipt to the release they audit: a passing
// receipt of attempt 1 is refused for a release built from attempt 2 once
// its commit, digest or attempt is named.
func TestReceiptVerifyRefusesAReceiptOfAnotherAttempt(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	for name, extra := range map[string][]string{
		"commit of attempt 2":  {"--commit", strings.Repeat("e", 40)},
		"digest of attempt 2":  {"--digest", "sha256:" + strings.Repeat("2", 64)},
		"attempt 2":            {"--attempt", "2"},
	} {
		args := append(claimArgs(f, f.rootKey, "run-1"), extra...)
		status, stdout, stderr := runWithOutput(t, append(args, f.bundle)...)
		if status != exitFailure || !strings.Contains(stderr, "GUARD_FAILED") || strings.Contains(stdout, "verified") {
			t.Errorf("%s: exit %d, %s%s", name, status, stdout, stderr)
		}
	}
	args := append(claimArgs(f, f.rootKey, "run-1"), "--commit", string(f.receipt.CandidateSHA),
		"--digest", string(f.receipt.CandidateDigest), "--attempt", "1", f.bundle)
	if status, _, stderr := runWithOutput(t, args...); status != exitOK {
		t.Errorf("the receipt's own commit, digest and attempt: exit %d, %s", status, stderr)
	}
}

// Input is read strictly: what one JSON reader could read differently from
// another, or what the command does not know, is refused rather than
// guessed at. A receipt that is unsigned, names another algorithm or is
// signed under a key the root did not delegate is refused, as is a root
// key that is not an ed25519 key. A revocation list that names the key
// refuses the receipt; one that does not is reported as checked.
func TestReceiptVerifyRefusesMalformedAndUnsignedInput(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	good, err := json.Marshal(iqa.ReceiptBundle{Receipt: f.receipt, Delegation: &f.delegation})
	if err != nil {
		t.Fatal(err)
	}
	text := string(good)
	withField := func(field string) string { return "{" + field + "," + text[1:] }
	unsigned := f.receipt
	unsigned.Signature = core.Signature{}
	none := f.receipt
	none.Signature.Algorithm = "none"
	for name, c := range map[string]struct {
		content string
		says    string
	}{
		"duplicate key":    {strings.Replace(text, `"score":28`, `"score":99,"score":28`, 1), "more than once"},
		"case-variant key": {strings.Replace(text, `"score":28`, `"score":99,"Score":28`, 1), "more than once"},
		"case-variant only": {strings.Replace(text, `"score":28`, `"Score":28`, 1), "not a field"},
		"unknown field":    {withField(`"note":"trust me"`), "not a field"},
		"unknown wrapper":  {`{"output":` + text + `,"extra":1}`, "not a field"},
		"trailing data":    {text + `{}`, "after the JSON document"},
		"json array":       {"[" + text + "]", "INVALID_ARGUMENT"},
		"truncated":        {text[:len(text)/2], "not valid JSON"},
		"unsigned":         {string(mustJSON(t, iqa.ReceiptBundle{Receipt: unsigned, Delegation: &f.delegation})), "INVALID_ARGUMENT"},
		"algorithm none":   {string(mustJSON(t, iqa.ReceiptBundle{Receipt: none, Delegation: &f.delegation})), "SIGNATURE_INVALID"},
	} {
		file := f.write(t, strings.ReplaceAll(name, " ", "-")+".json", []byte(c.content))
		status, stdout, stderr := runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), file)...)
		if status != exitFailure || !strings.Contains(stderr, c.says) || strings.Contains(stdout, "verified") {
			t.Errorf("%s: exit %d, want %d saying %q: %s%s", name, status, exitFailure, c.says, stdout, stderr)
		}
	}
	notEd25519 := f.write(t, "rsa.pub", []byte("-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA\n-----END PUBLIC KEY-----\n"))
	if status, _, stderr := runWithOutput(t, append(claimArgs(f, notEd25519, "run-1"), f.bundle)...); status == exitOK {
		t.Errorf("a root key that is not an ed25519 key was accepted: %s", stderr)
	}

	revoke := func(name string, sequence int64, keys ...string) string {
		list, err := delegation.SignRevocations(context.Background(), f.root, delegation.RevocationList{FactoryID: "acme-app",
			RootKeyID: f.delegation.Delegation.RootKeyID, Sequence: sequence, IssuedAt: time.Now().UTC(), KeyIDs: keys})
		if err != nil {
			t.Fatal(err)
		}
		return f.write(t, name, mustJSON(t, list))
	}
	status, _, stderr := runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--revocations", revoke("revoked.json", 2, f.delegation.Delegation.KeyID), f.bundle)...)
	if status != exitFailure || !strings.Contains(stderr, "revoked") {
		t.Errorf("a receipt signed by a revoked key: exit %d, %s", status, stderr)
	}
	status, stdout, stderr := runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--revocations", revoke("others.json", 3, "ed25519:other"), f.bundle)...)
	if status != exitOK || !strings.Contains(stdout, "checked against list 3") {
		t.Errorf("a revocation list that names another key: exit %d, %s%s", status, stdout, stderr)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
