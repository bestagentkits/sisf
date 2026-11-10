// Package signing holds the ed25519 primitives of one factory deployment:
// keys, a signer and a verifier that implement the shared contracts, and the
// signed release manifest.
//
// A signature here always covers a canonical signing payload, the bytes a
// signed type produces with core.PayloadBuilder. Those bytes name the type
// of record and the version of its field list, so a signature made for one
// kind of record never verifies as another, even with the same key. The
// signer and the verifier refuse any other bytes; they add no encoding of
// their own, and a signature is plain ed25519 over the payload.
//
// Private keys are never read from the repository and never printed. The
// trust anchor is the Owner's root public key, taken from configuration.
package signing

import (
	"bytes"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// recordOpening is the fixed first line of every canonical signing payload.
// It is taken from the payload builder itself, so it follows the contract.
var recordOpening = func() []byte {
	payload := core.NewPayloadBuilder("", 0).Bytes()
	return payload[:bytes.IndexByte(payload, '\n')+1]
}()

// isRecordPayload reports whether payload is a canonical signing payload,
// the only kind of bytes a key of the factory signs or accepts.
func isRecordPayload(payload []byte) bool {
	return len(recordOpening) > 1 && len(payload) > len(recordOpening) && bytes.HasPrefix(payload, recordOpening)
}

// sprintf is fmt.Sprintf, named once so the error helpers stay short.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// invalid builds the INVALID_ARGUMENT error used by this package.
func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, sprintf(format, args...))
}

// rejected builds the SIGNATURE_INVALID error. Only a nil error from a
// verifier means the payload is authentic.
func rejected(format string, args ...any) *core.Error {
	return core.NewError(core.CodeSignatureInvalid, sprintf(format, args...))
}
