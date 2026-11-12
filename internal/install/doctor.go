package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Verdict is the outcome of one doctor check.
type Verdict string

// The outcomes. A warning is something the Owner should know but that does
// not stop the factory from doing safe work; a failure does. A skipped check
// could not be run because of a failure reported on another line, such as a
// factory that is not running, so it neither passes nor fails twice.
const (
	VerdictOK   Verdict = "ok"
	VerdictWarn Verdict = "warn"
	VerdictFail Verdict = "fail"
	VerdictSkip Verdict = "skip"
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

// Counts returns how many checks ended in each verdict but skip.
func (r Report) Counts() (ok, warn, fail int) {
	for _, c := range r.Checks {
		switch c.Verdict {
		case VerdictOK:
			ok++
		case VerdictWarn:
			warn++
		case VerdictSkip:
		default:
			fail++
		}
	}
	return ok, warn, fail
}

// Skipped returns how many checks were skipped.
func (r Report) Skipped() int {
	n := 0
	for _, c := range r.Checks {
		if c.Verdict == VerdictSkip {
			n++
		}
	}
	return n
}

// Write prints the report for a person, one check per line.
func (r Report) Write(w io.Writer) error {
	for _, c := range r.Checks {
		lines := wrapWords(c.Detail, reportWidth-reportIndent)
		text := strings.Join(lines, "\n"+strings.Repeat(" ", reportIndent))
		if _, err := fmt.Fprintf(w, "%-5s %-24s %s\n", strings.ToUpper(string(c.Verdict)), c.Name, text); err != nil {
			return unavailablef(err, "the report could not be written")
		}
	}
	_, err := fmt.Fprintf(w, "\n%s\n", r.Summary())
	if err != nil {
		return unavailablef(err, "the report could not be written")
	}
	return nil
}

// The report is wrapped to reportWidth columns; a detail that goes on is
// indented under the first line's detail.
const (
	reportWidth  = 100
	reportIndent = 5 + 1 + 24 + 1
)

// wrapWords breaks text into lines of at most width columns, at spaces. A
// word longer than width, such as a path, keeps a line of its own.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

// Summary is the last line of the report, such as "12 ok, 1 warning,
// 1 failure, 5 skipped".
func (r Report) Summary() string {
	ok, warn, fail := r.Counts()
	text := fmt.Sprintf("%d ok, %s, %s", ok, plural(warn, "warning"), plural(fail, "failure"))
	if n := r.Skipped(); n > 0 {
		text += fmt.Sprintf(", %d skipped", n)
	}
	return text
}

// plural writes a count with its noun in the right number.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// collector gathers the checks of one run.
type collector struct{ report Report }

func (c *collector) add(name string, v Verdict, format string, args ...any) {
	for i, arg := range args {
		if err, ok := arg.(error); ok {
			args[i] = errorText(err)
		}
	}
	c.report.Checks = append(c.report.Checks, Check{Name: name, Verdict: v, Detail: sprintf(format, args...)})
}

func (c *collector) ok(name, format string, args ...any)   { c.add(name, VerdictOK, format, args...) }
func (c *collector) warn(name, format string, args ...any) { c.add(name, VerdictWarn, format, args...) }
func (c *collector) fail(name, format string, args ...any) { c.add(name, VerdictFail, format, args...) }
func (c *collector) skip(name, reason string)              { c.add(name, VerdictSkip, "skipped: %s", reason) }

// Doctor checks an install end to end and says what is wrong in words the
// Owner can act on. It changes nothing. Before any check that needs a
// running factory, it proves that what answers on the fixed ports is this
// install's factory; when the factory is not running, or another factory
// answers there, that is one failure with the next step, and the checks that
// would only repeat it are skipped.
func (in *Installer) Doctor(ctx context.Context) (Report, error) {
	var c collector
	m, err := in.Layout.LoadManifest()
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			c.fail("manifest", "%s holds no install (no factory.yaml): run `sisf init --dir %s` first", in.Layout.Dir, in.Layout.Dir)
		} else {
			c.fail("manifest", "%s cannot be read: %v", in.Layout.Config(), err)
		}
		return c.report, nil
	}
	in.checkManifest(&c, m)
	live := in.checkAnswering(ctx, &c, m)
	// Before the first start there is no release and no state database;
	// saying so twice more would hide the one line that matters.
	if live.ours || fileExists(in.Layout.ReleaseManifest()) {
		in.checkRelease(ctx, &c, m)
	} else {
		c.skip("release manifest", live.skip+"; `sisf up` builds and signs the release")
	}
	in.checkSecrets(&c, m)
	in.checkIdentity(&c, m)
	switch {
	case m.Mode != manifest.ModeDocker:
	case live.ours || fileExists(in.Layout.Compose()):
		in.checkCompose(&c)
	default:
		c.skip("compose", live.skip+"; `sisf up` renders the Compose file")
	}
	in.checkBudget(&c, m)
	if live.ours {
		in.checkZones(ctx, &c, m)
	} else {
		c.skip("zones", live.skip)
	}
	checkGitHubEvents(&c, m)
	if live.ours || m.Extensions["profile"] != profileSim {
		in.checkGitHub(ctx, &c, m)
	} else {
		c.skip("github", live.skip+"; the simulation's fake GitHub is one of its units")
	}
	if live.ours || m.Mode != manifest.ModeStandalone || fileExists(in.Layout.StateDB()) {
		in.checkState(ctx, &c, m)
	} else {
		c.skip("state store", live.skip+"; `sisf up` creates the database")
	}
	if live.ours {
		in.checkRollback(ctx, &c, m)
		in.checkTopology(ctx, &c, m)
	} else {
		c.skip("rollback", live.skip)
		c.skip("release topology", live.skip)
	}
	return c.report, ctx.Err()
}

// errorText is the message of err for a person: a typed error without its
// code, which is for programs and reads like a crash in a report.
func errorText(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Message
	}
	return err.Error()
}

// fileExists reports whether path names something that can be stat'ed.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
