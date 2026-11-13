package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxReceiptFile bounds a receipt, delegation or revocation file.
const maxReceiptFile = 1 << 20

func init() {
	register(Command{
		Path:    []string{"receipt", "verify"},
		Args:    "--root-key file --factory id --issue owner/repo#n --run id [--commit sha] [--delegation file] [--revocations file] <file|->",
		Summary: "Check a QA receipt's signature offline with the Owner's root public key",
		Help: `
Checks a QA receipt with nothing but the Owner's root public key; it connects
to no factory and trusts nothing the factory says. <file> (or - for standard
input) is what "sisf call runs.receipt" printed, its "output" object, or a
bare receipt with --delegation.

The receipt is accepted only when:

  - the delegation is signed by the root key in --root-key, for the factory
    in --factory, and names the key that signed the receipt as a key for
    QA receipts held by the supervisor;
  - the receipt is complete and its signature covers every field, so any
    change after signing is refused;
  - the key was within its delegated window when the receipt was issued and
    is not revoked by the root-signed list in --revocations;
  - the receipt is about --factory, --issue, --run and, when given,
    --commit.

The key is judged as of the receipt's issue time, so a receipt stays
verifiable after its week-long key has expired. A key that leaked could
then sign a receipt dated inside its old window; pass the Owner's latest
revocation list to rule that out. Without one the report says the
revocation check was not made.

On success it prints the root key, the signing key and its window, and every
signed field, and exits 0; any refusal exits 1 with its reason. The verdict,
reason and candidate the factory stored beside the receipt are not signed and
are not shown. --json prints the result as JSON.`,
		Flags: func(fs *flag.FlagSet) { bindReceiptFlags(fs, &receiptFlags{}) },
		Run:   runReceiptVerify,
	})
}

// receiptFlags are the options of sisf receipt verify.
type receiptFlags struct {
	rootKey, factory, issue, run, commit, delegation, revocations string
	asJSON                                                        bool
}

func bindReceiptFlags(fs *flag.FlagSet, f *receiptFlags) {
	fs.StringVar(&f.rootKey, "root-key", "", "`file` holding the Owner's root public key (PEM or one line of base64)")
	fs.StringVar(&f.factory, "factory", "", "factory `id` the receipt must be from")
	fs.StringVar(&f.issue, "issue", "", "issue (`owner/repo#n`) the receipt must be about")
	fs.StringVar(&f.run, "run", "", "run `id` the receipt must be about")
	fs.StringVar(&f.commit, "commit", "", "candidate commit `sha` the receipt must be about; not checked when empty")
	fs.StringVar(&f.delegation, "delegation", "", "`file` holding the signed delegation of the receipt's key, when the input carries none")
	fs.StringVar(&f.revocations, "revocations", "", "`file` holding the Owner's latest root-signed revocation list")
	fs.BoolVar(&f.asJSON, "json", false, "print the result as JSON")
}

func runReceiptVerify(ctx context.Context, env *Env, args []string) error {
	fs := newFlagSet("receipt verify")
	var f receiptFlags
	bindReceiptFlags(fs, &f)
	pos, err := parseInterspersed(fs, args, 1, "the receipt file, or - for standard input")
	if err != nil {
		return err
	}
	switch {
	case f.rootKey == "":
		return usagef("--root-key is required: the receipt is checked against the Owner's root public key")
	case f.factory == "" || f.issue == "" || f.run == "":
		return usagef("--factory, --issue and --run are required: they say what the receipt must be about")
	}
	want := iqa.ReceiptClaim{FactoryID: core.FactoryID(f.factory), RunID: core.RunID(f.run), CandidateSHA: core.CommitSHA(f.commit)}
	if want.IssueID, err = core.ParseIssueRef(f.issue); err != nil {
		return usagef("--issue: %v", err)
	}
	rootText, err := readBounded(env, f.rootKey, "the root public key")
	if err != nil {
		return err
	}
	root, err := signing.ParsePublicKey(string(rootText))
	if err != nil {
		return err
	}
	bundle, err := readReceiptBundle(env, pos[0])
	if err != nil {
		return err
	}
	if f.delegation != "" {
		var d delegation.SignedDelegation
		if err := readJSONFile(env, f.delegation, "the delegation", &d); err != nil {
			return err
		}
		bundle.Delegation = &d
	}
	var revocations *delegation.SignedRevocationList
	if f.revocations != "" {
		revocations = &delegation.SignedRevocationList{}
		if err := readJSONFile(env, f.revocations, "the revocation list", revocations); err != nil {
			return err
		}
	}
	check, err := iqa.VerifyReceiptBundle(ctx, root, bundle, want, revocations, time.Now())
	if err != nil {
		return err
	}
	if f.asJSON {
		out, err := json.MarshalIndent(struct {
			Verdict string `json:"verdict"`
			iqa.ReceiptCheck
		}{"verified", check}, "", "  ")
		if err != nil {
			return core.NewError(core.CodeInternal, "the result cannot be encoded").WithCause(err)
		}
		return env.Printf("%s\n", out)
	}
	return env.Printf("%s", receiptReport(check))
}

// readReceiptBundle reads the bundle runs.receipt hands out, the "output"
// object around it as sisf call prints it, or a bare receipt.
func readReceiptBundle(env *Env, path string) (iqa.ReceiptBundle, error) {
	raw, err := readBounded(env, path, "the receipt")
	if err != nil {
		return iqa.ReceiptBundle{}, err
	}
	var probe struct {
		Output  json.RawMessage `json:"output"`
		Receipt json.RawMessage `json:"receipt"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, "the receipt file is not JSON")
	}
	if len(probe.Output) > 0 {
		raw = probe.Output
		if err := json.Unmarshal(raw, &probe); err != nil {
			return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, "the output in the receipt file is not a JSON object")
		}
	}
	var b iqa.ReceiptBundle
	if len(probe.Receipt) > 0 {
		err = json.Unmarshal(raw, &b)
	} else {
		err = json.Unmarshal(raw, &b.Receipt)
	}
	if err != nil {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, "the receipt file does not hold a QA receipt")
	}
	return b, nil
}

// readJSONFile decodes one JSON file into out.
func readJSONFile(env *Env, path, what string, out any) error {
	raw, err := readBounded(env, path, what)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return core.NewError(core.CodeInvalidArgument, what+" is not readable JSON")
	}
	return nil
}

// readBounded reads a file, or standard input for "-", of at most
// maxReceiptFile bytes.
func readBounded(env *Env, path, what string) ([]byte, error) {
	r := env.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, core.NewError(core.CodeNotFound, what+" cannot be read from "+path).WithCause(err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxReceiptFile+1))
	switch {
	case err != nil:
		return nil, core.NewError(core.CodeUnavailable, what+" cannot be read").WithCause(err)
	case len(raw) > maxReceiptFile:
		return nil, core.NewError(core.CodeInvalidArgument, what+" is larger than the "+fmt.Sprint(maxReceiptFile)+" bytes it may be")
	}
	return raw, nil
}

// receiptReport renders a verified receipt: the keys, then every signed
// field.
func receiptReport(c iqa.ReceiptCheck) string {
	r := c.Receipt
	var b strings.Builder
	line := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "  %-22s %s\n", label+":", fmt.Sprintf(format, args...))
	}
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	b.WriteString("QA receipt verified: signed by a key the root key delegated, and about the run named.\n\n")
	line("root key", "%s", c.RootKeyID)
	line("signing key", "%s", c.KeyID)
	line("key delegation", "%s held by %s, valid %s to %s", c.Purpose, c.Holder, stamp(c.KeyNotBefore), stamp(c.KeyNotAfter))
	line("key judged at", "%s (the receipt's issue time)", stamp(c.JudgedAt))
	if c.RevocationSequence > 0 {
		line("revocations", "checked against list %d", c.RevocationSequence)
	} else {
		line("revocations", "not checked (no --revocations list given)")
	}
	b.WriteString("\nSigned fields:\n")
	line("factory", "%s", r.FactoryID)
	line("issue", "%s", r.IssueID)
	line("run", "%s", r.RunID)
	line("evaluator attempt", "%d", r.EvaluatorAttempt)
	line("candidate commit", "%s", r.CandidateSHA)
	line("candidate digest", "%s", r.CandidateDigest)
	line("suite digest", "%s", r.SuiteDigest)
	line("baseline commit", "%s", r.BaselineSHA)
	line("policy commit", "%s", r.PolicySHA)
	line("qa agent", "%s on %s", r.QAAgentIdentity, routeText(r.QAModelRoute))
	line("coding agent", "%s on %s", r.CodingAgentIdentity, routeText(r.CodingModelRoute))
	line("same model route", "%t", r.SameModelRoute)
	line("issued", "%s", stamp(r.IssuedAt))
	expiry := "still valid"
	if c.Expired {
		expiry = "expired; it can no longer authorize a release"
	}
	line("expires", "%s (%s)", stamp(r.ExpiresAt), expiry)
	gates := "all passed"
	if !c.HardGatesPassed {
		gates = "NOT all passed"
	}
	line("hard gates", "%s", gates)
	line("score", "%d", r.Score)
	b.WriteString("\nChecks:\n")
	for _, check := range r.Checks {
		result := "passed"
		if !check.Passed {
			result = "FAILED"
		}
		points := ""
		if check.Kind == core.CheckRubric {
			points = fmt.Sprintf(" %d/%d", check.Points, check.MaxPoints)
		}
		fmt.Fprintf(&b, "  %-10s %-28s %s%s\n", check.Kind, check.Name, result, points)
	}
	return b.String()
}

// routeText renders a model route as provider/model, with its effort.
func routeText(r core.ModelRoute) string {
	s := r.Provider + "/" + r.Model
	if r.Effort != "" {
		s += " (" + r.Effort + ")"
	}
	return s
}
