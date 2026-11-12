package install

import (
	"fmt"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
)

// invalidf builds the INVALID_ARGUMENT error used for bad options.
func invalidf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

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
