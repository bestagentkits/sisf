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

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/strictjson"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxReceiptFile bounds a receipt, delegation or revocation file.
const maxReceiptFile = 1 << 20

// The exit status of sisf receipt verify beside exitOK (authentic, hard
// gates passed and, under --require-unexpired, unexpired), exitFailure
// (refused) and exitUsage.
const (
	// exitGatesFailed is an authentic receipt whose hard gates did not pass.
	exitGatesFailed = 3
	// exitExpired is an authentic receipt that has expired, under
	// --require-unexpired: a passing one, or a failing one under
	// --allow-failed.
	exitExpired = 4
)

func init() {
	register(Command{
		Path: []string{"receipt", "verify"},
		Args: "--root-key file --root-key-id fingerprint --factory id --issue owner/repo#n --run id " +
			"[--commit sha] [--digest sha256:...] [--attempt n] [--delegation file] [--revocations file] " +
			"[--root-offline] [--allow-failed] [--require-unexpired] [--json] <file|->",
		Summary: "Check a QA receipt's signature offline with the Owner's root public key",
		Help: `
Checks a QA receipt with nothing but the Owner's root public key; it connects
to no factory. <file> (or - for standard input) is what "sisf call
runs.receipt" printed, its "output" object, or a bare receipt with
--delegation. Every file is read strictly: a key that appears twice (also in
another letter case), a key that is not a field, or data after the document
is refused.

What it proves, and what it does not: a receipt that verifies was signed
under a key the root key delegated. It does not protect against whoever
holds the root key, who can sign anything. In the default profile the root
key sits in the factory host's secret store, so the factory host can; the
report then states the assurance "lower_root_in_factory_store". Only when
the Owner keeps the root key off the host and tells you so apart from the
factory may you pass --root-offline, and the report states "root_offline".
Get the root key's fingerprint from the Owner the same way and pass it as
--root-key-id; a root key whose fingerprint differs is refused.

The receipt is accepted as authentic only when:

  - the root key in --root-key has the fingerprint in --root-key-id, when
    given;
  - the delegation is signed by that root key, for the factory in
    --factory, and names the key that signed the receipt as a key for QA
    receipts held by the supervisor;
  - the receipt is complete and its signature covers every field, so any
    change after signing is refused;
  - the key was within its delegated window when the receipt was issued and
    is not revoked by the root-signed list in --revocations;
  - the receipt is about --factory, --issue, --run and, when given,
    --commit, --digest (the candidate image digest) and --attempt (the
    evaluator attempt). Name all three to bind the receipt to the release
    you audit: a passing receipt of an earlier attempt is then refused.

The key is judged as of the receipt's issue time, so a receipt stays
verifiable after its week-long key has expired. A key that leaked could
then sign a receipt dated inside its old window; pass the Owner's latest
revocation list to rule that out. Without one the report says the
revocation check was not made.

Exit status:

  0  authentic, its hard gates passed and, under --require-unexpired, it
     has not expired; or authentic under --allow-failed
  1  refused: not authentic, malformed, or about something else
  2  usage error
  3  authentic, but its hard gates did NOT pass (not reported as verified)
  4  authentic, but expired, under --require-unexpired; also with
     --allow-failed, which never lets an expired receipt through

Expiry is always reported; it decides the exit status only under
--require-unexpired. --allow-failed exits 0 for an authentic receipt
whose gates failed, to inspect it. The report prints the root key, the
assurance, the signing key and its window, and every signed field. The
verdict, reason and candidate the factory stored beside the receipt are not
signed and are not shown. --json prints the result as JSON.`,
		Flags: func(fs *flag.FlagSet) { bindReceiptFlags(fs, &receiptFlags{}) },
		Run:   runReceiptVerify,
	})
}

// receiptFlags are the options of sisf receipt verify.
type receiptFlags struct {
	rootKey, rootKeyID, factory, issue, run, commit, digest, delegation, revocations string
	attempt                                                                          int
	rootOffline, allowFailed, requireUnexpired, asJSON                               bool
}

func bindReceiptFlags(fs *flag.FlagSet, f *receiptFlags) {
	fs.StringVar(&f.rootKey, "root-key", "", "`file` holding the Owner's root public key (PEM or one line of base64)")
	fs.StringVar(&f.rootKeyID, "root-key-id", "", "the root key's `fingerprint` (ed25519:...), got from the Owner apart from the factory")
	fs.StringVar(&f.factory, "factory", "", "factory `id` the receipt must be from")
	fs.StringVar(&f.issue, "issue", "", "issue (`owner/repo#n`) the receipt must be about")
	fs.StringVar(&f.run, "run", "", "run `id` the receipt must be about")
	fs.StringVar(&f.commit, "commit", "", "candidate commit `sha` the receipt must be about; not checked when empty")
	fs.StringVar(&f.digest, "digest", "", "candidate image `digest` (sha256:...) the receipt must be about; not checked when empty")
	fs.IntVar(&f.attempt, "attempt", 0, "evaluator attempt `n` the receipt must be of; not checked when 0")
	fs.StringVar(&f.delegation, "delegation", "", "`file` holding the signed delegation of the receipt's key, when the input carries none")
	fs.StringVar(&f.revocations, "revocations", "", "`file` holding the Owner's latest root-signed revocation list")
	fs.BoolVar(&f.rootOffline, "root-offline", false, "the Owner told you, apart from the factory, that the root key is kept off the factory host")
	fs.BoolVar(&f.allowFailed, "allow-failed", false, "exit 0 for an authentic receipt whose hard gates did not pass, to inspect it")
	fs.BoolVar(&f.requireUnexpired, "require-unexpired", false, "exit 4 for an authentic receipt that has expired")
	fs.BoolVar(&f.asJSON, "json", false, "print the result as JSON")
}

// receiptOutcome is what sisf receipt verify concluded about an authentic
// receipt.
type receiptOutcome struct {
	Verdict   string               `json:"verdict"`
	Assurance delegation.Assurance `json:"assurance"`
	// Pinned says the root key's fingerprint was checked against
	// --root-key-id.
	Pinned bool `json:"root_key_pinned"`
	iqa.ReceiptCheck
}

// The verdicts of an authentic receipt.
const (
	verdictVerified    = "verified"
	verdictGatesFailed = "authentic_hard_gates_failed"
	verdictExpired     = "authentic_expired"
)

// receiptNotAccepted is the exit status of an authentic receipt that is not
// accepted as passing. Its reason is already printed.
type receiptNotAccepted struct {
	code   int
	reason string
}

func (e receiptNotAccepted) Error() string { return e.reason }
func (e receiptNotAccepted) ExitCode() int { return e.code }
func (e receiptNotAccepted) Silent() bool  { return true }

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
	case f.attempt < 0:
		return usagef("--attempt must be 1 or more, or 0 not to check it")
	}
	// Fingerprints, commits and digests are hex: read them in lower case.
	f.rootKeyID, f.commit, f.digest = strings.ToLower(f.rootKeyID), strings.ToLower(f.commit), strings.ToLower(f.digest)
	if f.commit != "" {
		if err := core.CommitSHA(f.commit).Validate(); err != nil {
			return usagef("--commit: %v", err)
		}
	}
	if f.digest != "" {
		if err := core.Digest(f.digest).Validate(); err != nil {
			return usagef("--digest: %v", err)
		}
	}
	want := iqa.ReceiptClaim{FactoryID: core.FactoryID(f.factory), RunID: core.RunID(f.run), CandidateSHA: core.CommitSHA(f.commit),
		CandidateDigest: core.Digest(f.digest), EvaluatorAttempt: f.attempt}
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
	if f.rootKeyID != "" && signing.KeyID(root) != f.rootKeyID {
		return core.NewError(core.CodeSignatureInvalid, "the root key in "+f.rootKey+" has the fingerprint "+signing.KeyID(root)+
			", not the fingerprint "+f.rootKeyID+" given by --root-key-id")
	}
	bundle, err := readReceiptBundle(env, pos[0])
	if err != nil {
		return err
	}
	if f.delegation != "" {
		var d delegation.SignedDelegation
		if err := readStrictJSON(env, f.delegation, "the delegation", &d); err != nil {
			return err
		}
		bundle.Delegation = &d
	}
	var revocations *delegation.SignedRevocationList
	if f.revocations != "" {
		revocations = &delegation.SignedRevocationList{}
		if err := readStrictJSON(env, f.revocations, "the revocation list", revocations); err != nil {
			return err
		}
	}
	check, err := iqa.VerifyReceiptBundle(ctx, root, bundle, want, revocations, time.Now())
	if err != nil {
		return err
	}
	out := receiptOutcome{Verdict: verdictVerified, Assurance: delegation.AssuranceLower, Pinned: f.rootKeyID != "", ReceiptCheck: check}
	if f.rootOffline {
		out.Assurance = delegation.AssuranceRootOffline
	}
	// Expiry under --require-unexpired is judged whatever the gates say, so
	// --allow-failed never lets an expired receipt through it.
	var refusal error
	if !check.HardGatesPassed {
		out.Verdict = verdictGatesFailed
		if !f.allowFailed {
			refusal = receiptNotAccepted{exitGatesFailed, "the receipt is authentic, but its hard gates did NOT pass"}
		}
	}
	if refusal == nil && check.Expired && f.requireUnexpired {
		out.Verdict = verdictExpired
		refusal = receiptNotAccepted{exitExpired, "the receipt is authentic, but it expired at " + check.Receipt.ExpiresAt.UTC().Format(time.RFC3339)}
	}
	if f.asJSON {
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return core.NewError(core.CodeInternal, "the result cannot be encoded").WithCause(err)
		}
		if err := env.Printf("%s\n", encoded); err != nil {
			return err
		}
	} else if err := env.Printf("%s", receiptReport(out)); err != nil {
		return err
	}
	if refusal != nil {
		say(env.Stderr, "%s receipt verify: %s\n", program, refusal.Error())
	}
	return refusal
}

// receiptEnvelope is what sisf call prints around an action's output.
type receiptEnvelope struct {
	FactoryID core.FactoryID            `json:"factory_id,omitempty"`
	RootKeyID string                    `json:"root_key_id,omitempty"`
	Output    *runactions.ReceiptExport `json:"output"`
	AuditID   string                    `json:"audit_id,omitempty"`
}

// readReceiptBundle reads, strictly, the output of runs.receipt, the
// envelope sisf call prints around it, or a bare receipt.
func readReceiptBundle(env *Env, path string) (iqa.ReceiptBundle, error) {
	raw, err := readBounded(env, path, "the receipt")
	if err != nil {
		return iqa.ReceiptBundle{}, err
	}
	if err := strictjson.Scan(raw, "the receipt file"); err != nil {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, err.Error())
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, "the receipt file must hold a JSON object")
	}
	switch {
	case probe["output"] != nil:
		var envelope receiptEnvelope
		if err := decodeStrict(raw, "the receipt file", &envelope); err != nil {
			return iqa.ReceiptBundle{}, err
		}
		if envelope.Output == nil {
			return iqa.ReceiptBundle{}, core.NewError(core.CodeInvalidArgument, "the receipt file's output holds no QA receipt")
		}
		return envelope.Output.ReceiptBundle, nil
	case probe["verify_with"] != nil:
		var export runactions.ReceiptExport
		err = decodeStrict(raw, "the receipt file", &export)
		return export.ReceiptBundle, err
	case probe["receipt"] != nil:
		var b iqa.ReceiptBundle
		err = decodeStrict(raw, "the receipt file", &b)
		return b, err
	}
	var b iqa.ReceiptBundle
	err = decodeStrict(raw, "the receipt file", &b.Receipt)
	return b, err
}

// readStrictJSON reads one file and decodes it strictly into out.
func readStrictJSON(env *Env, path, what string, out any) error {
	raw, err := readBounded(env, path, what)
	if err != nil {
		return err
	}
	return decodeStrict(raw, what, out)
}

// decodeStrict decodes raw with strictjson.Decode, reporting a refusal as
// INVALID_ARGUMENT.
func decodeStrict(raw []byte, what string, out any) error {
	if err := strictjson.Decode(raw, what, out); err != nil {
		return core.NewError(core.CodeInvalidArgument, err.Error())
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
func receiptReport(o receiptOutcome) string {
	c := o.ReceiptCheck
	r := c.Receipt
	var b strings.Builder
	line := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "  %-22s %s\n", label+":", fmt.Sprintf(format, args...))
	}
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	switch o.Verdict {
	case verdictVerified:
		b.WriteString("QA receipt verified: signed by a key the root key delegated, about the run named, and its hard gates passed.\n")
	case verdictGatesFailed:
		b.WriteString("QA receipt authentic, but its hard gates did NOT pass: signed by a key the root key delegated, about the run named.\n")
	case verdictExpired:
		b.WriteString("QA receipt authentic and passing, but expired: it can no longer authorize a release.\n")
	}
	b.WriteString("This proves nothing against whoever holds the root key, who can sign anything.\n\n")
	pinned := "not pinned: pass --root-key-id with the fingerprint the Owner gave you"
	if o.Pinned {
		pinned = "matches --root-key-id"
	}
	line("root key", "%s (%s)", c.RootKeyID, pinned)
	assurance := "the root key may sit on the factory host, which could then sign anything"
	if o.Assurance == delegation.AssuranceRootOffline {
		assurance = "the Owner says the root key is kept off the factory host"
	}
	line("assurance", "%s (%s)", o.Assurance, assurance)
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
