//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// reportRelPath is where the run's report goes, relative to the repository
// root; the run that spends nothing writes upOnlyReportRelPath instead, so
// it never overwrites the report of a real run.
const (
	reportRelPath       = "plans/261110-1325-sisf-p0-p3-implementation/reports/real-smoke-run-report.md"
	upOnlyReportRelPath = "plans/261110-1325-sisf-p0-p3-implementation/reports/real-smoke-up-only-report.md"
)

func reportPath(upOnly bool) string {
	if upOnly {
		return upOnlyReportRelPath
	}
	return reportRelPath
}

// Check statuses of the report.
const (
	statusPass       = "pass"
	statusFail       = "fail"
	statusNotChecked = "not checked"
	// statusInfo is a note for the reader that passes or fails nothing.
	statusInfo = "info"
)

type namedValue struct{ Name, Value string }

type checkResult struct{ Name, Status, Evidence string }

type phaseTiming struct {
	Name string
	Took time.Duration
}

// stateVisit is one transition of the issue, read from the factory's journal.
type stateVisit struct {
	State string
	RunID string
	At    time.Time
}

// smokeReport is everything the run learned. It is written once, in a
// cleanup, whatever the outcome.
type smokeReport struct {
	Started, Finished time.Time
	Outcome           string
	Repo              string
	Versions          []namedValue
	Commands          []string
	IssueURL          string
	PullRequests      []string
	RunIDs            []string
	Phases            []phaseTiming
	States            []stateVisit
	Receipt           string
	Cost              string
	Checks            []checkResult
	Deviations        []string
	Evidence          []string
	Teardown          string
}

func (r *smokeReport) phase(name string, started time.Time) {
	r.Phases = append(r.Phases, phaseTiming{Name: name, Took: time.Since(started).Round(time.Second)})
}

func (r *smokeReport) check(name, status, evidence string) {
	r.Checks = append(r.Checks, checkResult{Name: name, Status: status, Evidence: evidence})
}

func (r *smokeReport) deviate(format string, args ...any) {
	r.Deviations = append(r.Deviations, fmt.Sprintf(format, args...))
}

// addUnique appends v to list unless it is empty or already there.
func addUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

// saigon is the report's time zone; a fixed UTC+7 stands in when the zone
// database is missing.
func saigon() *time.Location {
	if loc, err := time.LoadLocation("Asia/Saigon"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*60*60)
}

// markdown renders the report. The caller redacts the result.
func (r *smokeReport) markdown(loc *time.Location) string {
	var b strings.Builder
	at := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.In(loc).Format("2006-01-02 15:04:05 MST")
	}
	cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }
	list := func(title string, items []string) {
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		if len(items) == 0 {
			b.WriteString("- none\n")
		}
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	fmt.Fprintf(&b, "# Real smoke run report\n\n- Date: %s\n- Finished: %s\n- Outcome: **%s**\n- Repository: %s\n",
		at(r.Started), at(r.Finished), r.Outcome, r.Repo)
	b.WriteString("\n## Tool versions\n\n| Tool | Version |\n|---|---|\n")
	for _, v := range r.Versions {
		fmt.Fprintf(&b, "| %s | %s |\n", cell(v.Name), cell(v.Value))
	}
	list("Issue and pull requests", append([]string{"issue: " + orNone(r.IssueURL)}, prefixed("pull request: ", r.PullRequests)...))
	list("Run IDs", r.RunIDs)
	b.WriteString("\n## Timings per phase\n\n| Phase | Took |\n|---|---|\n")
	for _, p := range r.Phases {
		fmt.Fprintf(&b, "| %s | %s |\n", cell(p.Name), p.Took)
	}
	b.WriteString("\n## States visited (factory journal)\n\n| State | Run | Entered | Time in state |\n|---|---|---|---|\n")
	for i, s := range r.States {
		stay := "-"
		if i+1 < len(r.States) {
			stay = r.States[i+1].At.Sub(s.At).Round(time.Second).String()
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell(s.State), cell(s.RunID), at(s.At), stay)
	}
	fmt.Fprintf(&b, "\n## Receipt\n\n%s\n\n## Cost from the usage ledger\n\n%s\n", orNone(r.Receipt), orNone(r.Cost))
	b.WriteString("\n## Assertions\n\n| Check | Result | Evidence |\n|---|---|---|\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(c.Name), c.Status, cell(c.Evidence))
	}
	list("Deviations and failures", r.Deviations)
	fmt.Fprintf(&b, "\n## Teardown\n\n%s\n", orNone(r.Teardown))
	list("Command lines run", prefixed("`", r.Commands, "`"))
	if len(r.Evidence) > 0 {
		b.WriteString("\n## Evidence\n")
		for _, e := range r.Evidence {
			fmt.Fprintf(&b, "\n```text\n%s\n```\n", strings.TrimRight(e, "\n"))
		}
	}
	return b.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// prefixed wraps every item in prefix and an optional suffix.
func prefixed(prefix string, items []string, suffix ...string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = prefix + it + strings.Join(suffix, "")
	}
	return out
}

// writeReport renders the report, passes it through the scrubber and writes it.
func writeReport(path string, r *smokeReport, s *secretScrubber) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s.clean(r.markdown(saigon()))), 0o644)
}

// repoRoot is the module root, found from this file's location.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
