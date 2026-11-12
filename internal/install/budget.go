package install

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Where the confirmation lives in the state store. The dispatcher's budget
// gate refuses every claim until an entry exists here.
const (
	BudgetNamespace = "install"
	BudgetKey       = "budget_confirmation"
)

// microsPerUnit converts whole currency units to ledger micros.
const microsPerUnit = 1_000_000

// BudgetChoice is the Owner's answer to the install-time budget question:
// the default mode is unlimited, which is confirmed once, together with
// either an alert threshold or an explicit choice to have none.
type BudgetChoice struct {
	Confirmed bool
	// AlertThresholdMicros raises a notification when spending passes it. It
	// never stops anything.
	AlertThresholdMicros core.Micros
	// AlertOff records that the Owner chose to have no alert.
	AlertOff bool
}

// Validate rejects a confirmation that does not settle the alert question.
func (c BudgetChoice) Validate() error {
	switch {
	case !c.Confirmed && (c.AlertOff || c.AlertThresholdMicros != 0):
		return invalidf("an alert setting without the unlimited-budget confirmation")
	case !c.Confirmed:
		return nil
	case c.AlertOff == (c.AlertThresholdMicros > 0):
		return invalidf("confirm the budget with either an alert threshold above zero or no alert, not both and not neither")
	case c.AlertThresholdMicros < 0:
		return invalidf("the alert threshold must not be negative")
	}
	return nil
}

// BudgetConfirmation is the record of a confirmation.
type BudgetConfirmation struct {
	Mode                 core.BudgetMode `json:"mode"`
	AlertThresholdMicros core.Micros     `json:"alert_threshold_micros,omitempty"`
	AlertOff             bool            `json:"alert_off,omitempty"`
	ConfirmedAt          time.Time       `json:"confirmed_at"`
}

// confirmation turns a validated choice into a record; ok is false when the
// Owner has not confirmed.
func (c BudgetChoice) confirmation(at time.Time) (BudgetConfirmation, bool) {
	if !c.Confirmed {
		return BudgetConfirmation{}, false
	}
	return BudgetConfirmation{
		Mode: core.BudgetUnlimited, AlertThresholdMicros: c.AlertThresholdMicros,
		AlertOff: c.AlertOff, ConfirmedAt: at.UTC(),
	}, true
}

// writeBudgetConfirmation stores the record beside the install.
func writeBudgetConfirmation(path string, rec BudgetConfirmation) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return invalidf("the budget confirmation cannot be encoded")
	}
	return writeSecret(path, append(data, '\n'))
}

// readBudgetConfirmation loads the record; ok is false when none was written.
func readBudgetConfirmation(path string) (rec BudgetConfirmation, ok bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, unavailablef(err, "cannot read %s", path)
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.Mode != core.BudgetUnlimited {
		return rec, false, invalidf("%s is not a budget confirmation", path)
	}
	return rec, true, nil
}

// budgetNotice is what the installer shows before it asks.
const budgetNotice = `Budget: the default mode is "unlimited".
That means SISF applies no monetary limit of its own. It is not a promise
that your model provider will not charge you, and every task still has a
timeout, a maximum number of attempts and a concurrency cap.
Until you confirm, the factory accepts issues but does not start any work.
`

// Prompt shows the unlimited default and asks the Owner to confirm it once,
// then asks for an alert threshold. Anything but the word "unlimited" leaves
// the budget unconfirmed, as does a closed input; that is not an error, it
// is the safe outcome. A threshold that cannot be read is an error.
func Prompt(in io.Reader, out io.Writer) (BudgetChoice, error) {
	r := bufio.NewReader(in)
	_, _ = io.WriteString(out, budgetNotice)
	_, _ = io.WriteString(out, `Type "unlimited" to confirm it: `)
	answer, _ := r.ReadString('\n') // a closed input leaves the answer empty
	if strings.TrimSpace(answer) != "unlimited" {
		_, _ = io.WriteString(out, "\nNot confirmed. The factory will only take in issues.\n")
		return BudgetChoice{}, nil
	}
	_, _ = io.WriteString(out, "Alert threshold in currency units (for example 50), or \"none\" for no alert: ")
	answer, _ = r.ReadString('\n')
	return ParseAlert(strings.TrimSpace(answer))
}

// ParseAlert reads an alert answer into a confirmed choice: "none" for no
// alert, or a positive amount in whole or decimal currency units.
func ParseAlert(answer string) (BudgetChoice, error) {
	if strings.EqualFold(answer, "none") {
		return BudgetChoice{Confirmed: true, AlertOff: true}, nil
	}
	units, err := strconv.ParseFloat(answer, 64)
	if err != nil || math.IsNaN(units) || math.IsInf(units, 0) || units <= 0 || units > 1e9 {
		return BudgetChoice{}, invalidf("the alert threshold must be a positive amount, or \"none\"")
	}
	return BudgetChoice{Confirmed: true, AlertThresholdMicros: core.Micros(math.Round(units * microsPerUnit))}, nil
}
