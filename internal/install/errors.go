package install

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
)

// invalidf builds the INVALID_ARGUMENT error used for bad options.
func invalidf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

// errRefused marks a refusal that starting again cannot change: the
// install or the invocation is wrong, not the moment.
var errRefused = errors.New("refused: starting again cannot change the outcome")

// refusedf builds the INVALID_ARGUMENT error of an `up` that can never
// succeed as configured. A caller run by a service manager answers it with
// a status the manager does not restart on.
func refusedf(format string, args ...any) *core.Error {
	return invalidf(format, args...).WithCause(errRefused)
}

// Refused reports whether err is a refusal that starting again, with the
// same install and the same invocation, cannot change.
func Refused(err error) bool { return errors.Is(err, errRefused) }

// unavailablef builds the UNAVAILABLE error for a failed outside action.
func unavailablef(cause error, format string, args ...any) *core.Error {
	return core.NewError(core.CodeUnavailable, fmt.Sprintf(format, args...)).WithCause(cause)
}

// violationsError turns broken isolation rules into one refusal.
func violationsError(vs []Violation) error {
	text := ""
	for _, v := range vs {
		text += "\n  " + v.String()
	}
	return core.NewError(core.CodePolicyHold, "the install breaks the isolation rules, so nothing was started:"+text)
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
