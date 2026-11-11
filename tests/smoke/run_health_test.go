//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/install"
)

// How the run learns, from the factory's own records, that the issue
// cannot close and waiting longer only burns the deadline:
//   - runs.inspect shows the run exhausted: it used every coding attempt;
//   - dispatcher.log records the run stopping after its last attempt, or an
//     attempt failing whose number reaches the manifest's attempt limit;
//   - `sisf up`'s output records a unit exiting or failing to start, which
//     a healthy run never shows (a deliberate restart logs "stopped").

// Lines the dispatcher writes (cmd/factoryd/wiring/dispatcher_tick_log.go).
const (
	dispatcherExhaustedMsg = `msg="a run used every attempt and stopped`
	dispatcherFailedMsg    = `msg="a coding attempt failed"`
)

var (
	attemptField = regexp.MustCompile(`\battempt=(\d+)\b`)
	// unitTrouble matches what `sisf up` logs when a unit dies or cannot
	// start (internal/install/procgroup.go).
	unitTrouble = regexp.MustCompile(`(?m)^(\S+ exited \(.*|\S+ cannot start: .*)$`)
)

// cannotFinish returns why the factory cannot finish the run, or "" while
// it may still.
func cannotFinish(runStatus, upLog, dispatcherLog string, maxAttempts int) string {
	if runStatus == "exhausted" {
		return "the run used every coding attempt (runs.inspect shows it exhausted)"
	}
	for line := range strings.SplitSeq(dispatcherLog, "\n") {
		if strings.Contains(line, dispatcherExhaustedMsg) {
			return "the run used every coding attempt (dispatcher.log: " + strings.TrimSpace(line) + ")"
		}
		if !strings.Contains(line, dispatcherFailedMsg) {
			continue
		}
		if m := attemptField.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && maxAttempts > 0 && n >= maxAttempts {
				return fmt.Sprintf("coding attempt %d of %d failed (dispatcher.log: %s)", n, maxAttempts, strings.TrimSpace(line))
			}
		}
	}
	if m := unitTrouble.FindString(upLog); m != "" {
		return "a unit of sisf up stopped: " + m
	}
	return ""
}

// factoryCannotFinish reads the unit logs of the install and the up log and
// says why the run cannot finish, or "".
func factoryCannotFinish(o *factoryObserver, upLog, runStatus string) string {
	dispatcherLog, _ := os.ReadFile(filepath.Join(install.Layout{Dir: o.home}.Logs(), "dispatcher.log"))
	up, _ := os.ReadFile(upLog)
	return cannotFinish(runStatus, string(up), string(dispatcherLog), o.maxAttempts)
}
