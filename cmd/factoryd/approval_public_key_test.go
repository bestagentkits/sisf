package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/signing"
)

// The supervisor verifies approvals with the public half of the control
// zone's approval key, which this command prints and nothing else.
func TestTheApprovalPublicKeyIsPrintedWithoutThePrivateKey(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seed := base64.RawURLEncoding.EncodeToString(priv.Seed())
	status, stdout, stderr := runWith(t, map[string]string{wiring.EnvApprovalKey: seed}, "approval-public-key")
	if status != exitOK || stdout != signing.EncodePublicKey(pub) {
		t.Fatalf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, seed) {
		t.Fatal("the approval key was printed")
	}
	if status, _, _ := runWith(t, nil, "approval-public-key"); status != exitFailure {
		t.Fatalf("without the key the command exited %d", status)
	}
}
