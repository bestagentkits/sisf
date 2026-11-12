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
// either the unlimited default, confirmed once together with an alert
// threshold or an explicit choice to have none, or a cap, with an optional
// alert threshold below it and the amount each run sets aside.
type BudgetChoice struct {
	Confirmed bool
	// CapMicros, when above zero, makes the budget capped: the factory admits
	// no run whose reservation would pass it.
	CapMicros core.Micros
	// RunEstimateMicros is what each run sets aside from a capped budget
	// before it is claimed; it defaults to a fifth of the cap.
	RunEstimateMicros core.Micros
	// AlertThresholdMicros raises a notification when spending passes it. It
	// never stops anything.
	AlertThresholdMicros core.Micros
	// AlertOff records that the Owner chose to have no alert.
	AlertOff bool
}

// Capped reports whether the choice is a capped budget.
func (c BudgetChoice) Capped() bool { return c.CapMicros > 0 }

// Validate rejects a confirmation that does not settle the alert question,
// and a cap that cannot be enforced.
func (c BudgetChoice) Validate() error {
	switch {
	case c.CapMicros < 0 || c.RunEstimateMicros < 0 || c.AlertThresholdMicros < 0:
		return invalidf("budget amounts must not be negative")
	case !c.Confirmed && (c.AlertOff || c.AlertThresholdMicros != 0 || c.CapMicros != 0 || c.RunEstimateMicros != 0):
		return invalidf("a budget setting without a confirmed budget")
	case !c.Confirmed:
		return nil
	case c.Capped() && c.AlertOff:
		return invalidf("a capped budget takes an alert threshold or none; there is no alert to turn off")
	case c.Capped() && c.AlertThresholdMicros >= c.CapMicros:
		return invalidf("the alert threshold must be below the budget cap")
	case c.Capped() && c.RunEstimateMicros > c.CapMicros:
		return invalidf("the run estimate must not be above the budget cap")
	case c.Capped():
		return nil
	case c.RunEstimateMicros != 0:
		return invalidf("a run estimate applies to a capped budget only")
	case c.AlertOff == (c.AlertThresholdMicros > 0):
		return invalidf("confirm the budget with either an alert threshold above zero or no alert, not both and not neither")
	}
	return nil
}

// withDefaults fills the run estimate of a capped budget.
func (c BudgetChoice) withDefaults() BudgetChoice {
	if c.Capped() && c.RunEstimateMicros == 0 {
		c.RunEstimateMicros = max(c.CapMicros/5, 1)
	}
	return c
}

// BudgetConfirmation is the record of a confirmation.
type BudgetConfirmation struct {
	Mode                 core.BudgetMode `json:"mode"`
	CapMicros            core.Micros     `json:"cap_micros,omitempty"`
	RunEstimateMicros    core.Micros     `json:"run_estimate_micros,omitempty"`
	AlertThresholdMicros core.Micros     `json:"alert_threshold_micros,omitempty"`
	AlertOff             bool            `json:"alert_off,omitempty"`
	ConfirmedAt          time.Time       `json:"confirmed_at"`
}

// valid reports whether the record is one this installer writes.
func (r BudgetConfirmation) valid() bool {
	switch r.Mode {
	case core.BudgetUnlimited:
		return r.CapMicros == 0
	case core.BudgetCapped:
		return r.CapMicros > 0 && r.RunEstimateMicros > 0
	}
	return false
}

// confirmation turns a validated choice into a record; ok is false when the
// Owner has not confirmed.
func (c BudgetChoice) confirmation(at time.Time) (BudgetConfirmation, bool) {
	if !c.Confirmed {
		return BudgetConfirmation{}, false
	}
	c = c.withDefaults()
	rec := BudgetConfirmation{
		Mode: core.BudgetUnlimited, AlertThresholdMicros: c.AlertThresholdMicros,
		AlertOff: c.AlertOff, ConfirmedAt: at.UTC(),
	}
	if c.Capped() {
		rec.Mode, rec.CapMicros, rec.RunEstimateMicros = core.BudgetCapped, c.CapMicros, c.RunEstimateMicros
	}
	return rec, true
}

// choice turns a stored record back into the Owner's choice.
func (r BudgetConfirmation) choice() BudgetChoice {
	return BudgetChoice{Confirmed: true, CapMicros: r.CapMicros, RunEstimateMicros: r.RunEstimateMicros,
		AlertThresholdMicros: r.AlertThresholdMicros, AlertOff: r.AlertOff}
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
	if err := json.Unmarshal(data, &rec); err != nil || !rec.valid() {
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
	micros, err := ParseAmount(answer)
	if err != nil {
		return BudgetChoice{}, invalidf("the alert threshold must be a positive amount, or \"none\"")
	}
	return BudgetChoice{Confirmed: true, AlertThresholdMicros: micros}, nil
}

// ParseAmount reads a positive amount in whole or decimal currency units
// into micros.
func ParseAmount(answer string) (core.Micros, error) {
	units, err := strconv.ParseFloat(strings.TrimSpace(answer), 64)
	if err != nil || math.IsNaN(units) || math.IsInf(units, 0) || units <= 0 || units > 1e9 {
		return 0, invalidf("%q is not a positive amount in currency units", answer)
	}
	return core.Micros(math.Round(units * microsPerUnit)), nil
}
