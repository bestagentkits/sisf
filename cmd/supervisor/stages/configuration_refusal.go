package stages

import (
	"errors"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ErrConfiguration marks a refusal the configuration alone decides: a
// flag, the manifest, or a backend chosen that cannot do the work. Starting
// again with the same configuration refuses again, so the supervisor ends
// such a start with a status `sisf up` does not restart on. What the
// environment reports (a sandbox network not there yet, a daemon that does
// not answer) is never marked, whatever its code: it may change by the next
// start.
var ErrConfiguration = errors.New("refused by the configuration")

// Misconfigured marks e as a refusal the configuration alone decides.
func Misconfigured(e *core.Error) *core.Error { return e.WithCause(ErrConfiguration) }
