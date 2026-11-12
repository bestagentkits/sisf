package main

import (
	"crypto/ed25519"
	"fmt"
	"io"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// runApprovalPublicKey prints the public half of the approval key in the
// control zone's environment, the value the supervisor verifies approvals
// with (SISF_APPROVAL_PUBLIC_KEY). The private key is never printed.
func runApprovalPublicKey(args []string, getenv func(string) string, stdout io.Writer) error {
	if err := parse(newFlags("approval-public-key"), args); err != nil {
		return err
	}
	key, err := wiring.ParseApprovalKey(core.NewSecretValue([]byte(getenv(wiring.EnvApprovalKey))))
	if err != nil {
		return err
	}
	defer clear(key)
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return core.NewError(core.CodeInternal, "the approval key has no ed25519 public key")
	}
	_, err = fmt.Fprint(stdout, signing.EncodePublicKey(pub))
	return err
}
