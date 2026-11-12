package stages

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"runtime"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// SimulatedImages is the image builder of the trusted simulation profile,
// where the coder is the scripted fake agent: no code is run, and the image
// digest is derived from the factory, the unit and the commit, so the same
// commit always names the same image.
type SimulatedImages struct {
	// Suite judges a commit in place of a test run; nil passes every commit
	// and says so in the evidence.
	Suite func(ctx context.Context, req ImageRequest) (passed bool, evidence string)
}

// Name names the builder in build records.
func (SimulatedImages) Name() string { return "simulated" }

// BuildImage derives the simulated image of req.
func (s SimulatedImages) BuildImage(ctx context.Context, req ImageRequest) (ImageResult, error) {
	sum := sha256.Sum256([]byte("sisf.simulated_image\n" + string(req.FactoryID) + "\n" + req.Unit + "\n" + string(req.Commit)))
	digest := core.Digest("sha256:" + hex.EncodeToString(sum[:]))
	passed, evidence := true, "simulation profile: scripted patches run no suite"
	if s.Suite != nil {
		passed, evidence = s.Suite(ctx, req)
	}
	return ImageResult{
		Digest: digest, Image: "sim.local/" + string(req.FactoryID) + "/" + req.Unit + "@" + string(digest),
		TestsPassed: passed, TestEvidence: evidence,
		SBOMRef: "sbom:" + string(digest), ProvenanceRef: "provenance:" + string(digest),
	}, nil
}

// The phases of a sandboxed build. The build command is run once per
// phase, each time in a fresh container of the sandbox, with the phase in
// SISF_PHASE:
//
//   - test runs the candidate's tests. Only its exit status is read: 0
//     means the tests passed.
//   - build builds the candidate. Only its exit status is read: anything
//     but 0 refuses the candidate.
//   - report runs no code of the candidate. It finds what the build phase
//     made, such as by asking the registry it was pushed to or by building
//     the commit again deterministically without running it, and reports
//     the image, its digest and the references of its SBOM and provenance
//     on lines "sisf-report <nonce> <key>: <value>", with the keys
//     image-digest, image, sbom and provenance, each exactly once.
//
// Where the deploy backend runs programs (SandboxImages.Programs), the
// report phase is also told the supervisor's own operating system and
// architecture, in SISF_REPORT_TARGET_OS and SISF_REPORT_TARGET_ARCH as Go
// names them, and exports the unit's one program built for them on lines
// "sisf-report <nonce> program: <base64>": any number of lines, in order,
// each decoding by itself. The supervisor hashes the bytes it receives, and
// image-digest must be the sha256 of exactly those bytes: the digest the
// build record, the QA receipt and the signed artifact carry is then the
// digest of the program that runs. Nothing else leaves the sandbox: no
// mount, no archive, no service.
//
// Candidate code runs in the first two phases and can print anything on
// their output, so nothing they print is read. The report phase is told the
// candidate under names of its own (SISF_REPORT_COMMIT, SISF_REPORT_UNIT,
// SISF_REPORT_BRANCH), so a build command that ignores the phase cannot find
// the candidate there and run its code, and the nonce, made after the
// candidate's code last ran, is all that marks a line as the reporter's
// own, so text of the candidate's that the reporter echoes is not read
// either.
const (
	phaseTest   = "test"
	phaseBuild  = "build"
	phaseReport = "report"
)

// SandboxImages builds images by running a build command in a sandbox of
// an isolating execution backend, phase by phase (see phaseTest). Every
// phase gets the time the build has left of Timeout.
type SandboxImages struct {
	Exec  execution.Backend
	Specs SandboxSpecs
	Argv  []string
	// Timeout bounds the whole build, every phase together.
	Timeout time.Duration
	// Programs keeps the program each build exports, for a deploy backend
	// that runs programs. Nil where the backend runs images or nothing: a
	// build then exports nothing and is told no target.
	Programs *programstore.Store
}

// Name names the builder in build records.
func (s SandboxImages) Name() string { return "sandbox:" + s.Exec.Name() }

// BuildImage runs the build command for req, phase by phase.
//
// The backend's own signal tells an outage from the candidate's fault: an
// error means a phase could not be run or observed, an outage that spends
// no attempt; a result of a phase where the candidate's code ran is the
// candidate's, so a non-zero exit of the build or a phase overrunning the
// time is refused. A report the command does not make as asked is the
// configuration's fault, an outage. A build cut short because the
// supervisor is stopping is neither.
func (s SandboxImages) BuildImage(ctx context.Context, req ImageRequest) (ImageResult, error) {
	if len(s.Argv) == 0 || s.Timeout <= 0 {
		return ImageResult{}, core.NewError(core.CodeInvalidArgument, "the sandboxed build needs a command and a timeout")
	}
	caps, err := s.Exec.Capabilities(ctx)
	if err != nil {
		return ImageResult{}, err
	}
	if req.Untrusted && !execution.UntrustedCodeAllowed(caps) {
		return ImageResult{}, untrustedRefused(s.Exec.Name())
	}
	spec := s.Specs.ForRun(req.RunID)
	sandbox, err := s.Exec.CreateSandbox(ctx, spec)
	if err != nil {
		return ImageResult{}, err
	}
	defer func() { _ = s.Exec.Destroy(context.WithoutCancel(ctx), sandbox.ID) }()
	deadline := time.Now().Add(s.Timeout)
	candidate := map[string]string{"SISF_COMMIT": string(req.Commit), "SISF_UNIT": req.Unit, "SISF_BRANCH": req.Branch}

	tested, err := s.phase(ctx, sandbox.ID, phaseTest, candidate, deadline, nil)
	if err != nil {
		return ImageResult{}, err
	}
	built, err := s.phase(ctx, sandbox.ID, phaseBuild, candidate, deadline, nil)
	if err != nil {
		return ImageResult{}, err
	}
	if built.ExitCode != 0 {
		r := refuse(core.CodeGuardFailed, RefusalBuildFailed, "",
			"the build command exited with status "+strconv.Itoa(built.ExitCode)+" on the candidate")
		r.err.Details["exit_code"] = strconv.Itoa(built.ExitCode)
		return ImageResult{}, r
	}
	// The nonce is made only now, after the candidate's code last ran.
	nonce, err := reportNonce()
	if err != nil {
		return ImageResult{}, err
	}
	env := map[string]string{
		"SISF_REPORT_COMMIT": string(req.Commit), "SISF_REPORT_UNIT": req.Unit, "SISF_REPORT_BRANCH": req.Branch,
		"SISF_REPORT_NONCE": nonce,
	}
	report := newReportReader(nonce, nil)
	var stream io.Writer
	if s.Programs != nil {
		// The program is too large to capture: the report is read as it
		// is streamed, the program going straight to the store.
		intake, err := s.Programs.Receive()
		if err != nil {
			return ImageResult{}, err
		}
		defer intake.Abort()
		report = newReportReader(nonce, intake)
		stream = report
		env["SISF_REPORT_TARGET_OS"], env["SISF_REPORT_TARGET_ARCH"] = runtime.GOOS, runtime.GOARCH
	}
	reported, err := s.phase(ctx, sandbox.ID, phaseReport, env, deadline, stream)
	if err != nil {
		return ImageResult{}, err
	}
	if reported.ExitCode != 0 {
		return ImageResult{}, reportUnusable("the report phase exited with status " + strconv.Itoa(reported.ExitCode))
	}
	if stream == nil {
		_, _ = report.Write(reported.Stdout) // the reader's Write never fails
	}
	out, err := report.result()
	if err != nil {
		return ImageResult{}, err
	}
	out.TestsPassed = tested.ExitCode == 0
	out.TestEvidence = "sandbox test phase exited with status " + strconv.Itoa(tested.ExitCode)
	return out, nil
}

// phase runs the build command for one phase within what is left of the
// build's time. A phase the backend could not run or observe is its error;
// one that ran out of time, or found none left, is refused as the
// candidate's, since its tests and build are what spend the time. Output is
// captured into the result unless stdout is given to stream it to.
func (s SandboxImages) phase(ctx context.Context, id execution.SandboxID, phase string, env map[string]string, deadline time.Time, stdout io.Writer) (execution.ExecResult, error) {
	left := time.Until(deadline)
	if left <= 0 {
		return execution.ExecResult{}, buildOverran(s.Timeout)
	}
	env = maps.Clone(env)
	env["SISF_PHASE"] = phase
	result, err := s.Exec.Exec(ctx, id, execution.Command{Argv: s.Argv, Timeout: left, Env: env, Stdout: stdout})
	switch {
	case ctx.Err() != nil:
		return execution.ExecResult{}, core.NewError(core.CodeUnavailable, "the sandboxed build was interrupted").WithCause(ctx.Err())
	case core.HasCode(err, core.CodeLimitExceeded):
		// The sandbox used up its time.
		return execution.ExecResult{}, buildOverran(s.Timeout)
	case err != nil:
		return execution.ExecResult{}, err
	case result.TimedOut:
		return execution.ExecResult{}, buildOverran(s.Timeout)
	}
	return result, nil
}

// buildOverran refuses a candidate whose build did not finish in time.
func buildOverran(timeout time.Duration) *PatchRefusal {
	return refuse(core.CodeTimeout, RefusalBuildTimeout, "", "the build command did not finish on the candidate within "+timeout.String())
}

// reportNonce makes the nonce of one report: 128 random bits.
func reportNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", core.NewError(core.CodeInternal, "a report nonce cannot be made").WithCause(err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// untrustedRefused is the typed refusal to run untrusted code on a backend
// that cannot isolate it.
func untrustedRefused(backend string) *core.Error {
	return core.NewError(core.CodePermissionDenied,
		"the "+backend+" execution backend cannot isolate untrusted code; configure the docker execution backend")
}
