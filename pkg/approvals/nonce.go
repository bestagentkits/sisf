package approvals

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/bestagentkits/sisf/pkg/core"
)

// NewNonce returns a fresh random nonce for an approval request: 128 bits
// from the system's secure source, as 32 hexadecimal characters.
func NewNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", core.NewError(core.CodeUnavailable, "no secure randomness is available for a nonce").WithCause(err)
	}
	return hex.EncodeToString(raw), nil
}
