package install

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Verdict is the outcome of one doctor check.
type Verdict string

// The three outcomes. A warning is something the Owner should know but that
// does not stop the factory from doing safe work; a failure does.
const (
	VerdictOK   Verdict = "ok"
	VerdictWarn Verdict = "warn"
	VerdictFail Verdict = "fail"
)

// Check is one line of the doctor's report. Detail holds facts a person can
// act on and never a secret: tokens and keys are checked, not echoed.
type Check struct {
	Name    string
	Verdict Verdict
	Detail  string
}

// Report is the result of a doctor run, in the order the checks ran.
type Report struct {
	Checks []Check
}

// OK is true when no check failed. Warnings do not fail a report.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Verdict == VerdictFail {
			return false
		}
	}
	return true
}

// Counts returns how many checks ended in each verdict.
func (r Report) Counts() (ok, warn, fail int) {
	for _, c := range r.Checks {
		switch c.Verdict {
		case VerdictOK:
			ok++
		case VerdictWarn:
			warn++
		default:
			fail++
		}
	}
	return ok, warn, fail
}

// Write prints the report for a person, one check per line.
func (r Report) Write(w io.Writer) error {
	for _, c := range r.Checks {
		if _, err := fmt.Fprintf(w, "%-5s %-24s %s\n", strings.ToUpper(string(c.Verdict)), c.Name, c.Detail); err != nil {
			return unavailablef(err, "the report could not be written")
		}
	}
	ok, warn, fail := r.Counts()
	_, err := fmt.Fprintf(w, "\n%d ok, %d warnings, %d failures\n", ok, warn, fail)
	if err != nil {
		return unavailablef(err, "the report could not be written")
	}
	return nil
}

// collector gathers the checks of one run.
type collector struct{ report Report }

func (c *collector) add(name string, v Verdict, format string, args ...any) {
	c.report.Checks = append(c.report.Checks, Check{Name: name, Verdict: v, Detail: sprintf(format, args...)})
}

func (c *collector) ok(name, format string, args ...any)   { c.add(name, VerdictOK, format, args...) }
func (c *collector) warn(name, format string, args ...any) { c.add(name, VerdictWarn, format, args...) }
func (c *collector) fail(name, format string, args ...any) { c.add(name, VerdictFail, format, args...) }

// Doctor checks an install end to end and says what is wrong in words the
// Owner can act on. It changes nothing. The checks that need a running
// factory report a failure when the factory is down, with the command that
// starts it, rather than being skipped.
func (in *Installer) Doctor(ctx context.Context) (Report, error) {
	var c collector
	m, err := in.Layout.LoadManifest()
	if err != nil {
		c.fail("manifest", "%s has no readable factory manifest: run `sisf init` first (%v)", in.Layout.Dir, err)
		return c.report, nil
	}
	in.checkManifest(&c, m)
	in.checkRelease(ctx, &c, m)
	in.checkSecrets(&c, m)
	in.checkIdentity(&c, m)
	if m.Mode == manifest.ModeDocker {
		in.checkCompose(&c)
	}
	in.checkBudget(&c, m)
	in.checkZones(ctx, &c, m)
	in.checkGitHub(ctx, &c, m)
	in.checkState(ctx, &c, m)
	in.checkRollback(ctx, &c, m)
	in.checkTopology(ctx, &c, m)
	return c.report, ctx.Err()
}
