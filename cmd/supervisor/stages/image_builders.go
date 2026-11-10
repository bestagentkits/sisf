package stages

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

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

// Output lines a sandboxed build command reports its result on.
const (
	outImageDigest = "sisf-image-digest:"
	outImage       = "sisf-image:"
	outTests       = "sisf-tests:"
	outSBOM        = "sisf-sbom:"
	outProvenance  = "sisf-provenance:"
)

// SandboxImages builds images by running a build command in a sandbox of
// an isolating execution backend. The command gets the commit and the unit
// in SISF_COMMIT and SISF_UNIT and reports its result on lines of the form
// "sisf-image-digest: sha256:...", "sisf-image: ref", "sisf-tests: passed",
// "sisf-sbom: ref" and "sisf-provenance: ref".
type SandboxImages struct {
	Exec  execution.Backend
	Specs SandboxSpecs
	Argv  []string
	// Timeout bounds the build command.
	Timeout time.Duration
}

// Name names the builder in build records.
func (s SandboxImages) Name() string { return "sandbox:" + s.Exec.Name() }

// BuildImage runs the build command for req.
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
	result, err := s.Exec.Exec(ctx, sandbox.ID, execution.Command{
		Argv: s.Argv, Timeout: s.Timeout,
		Env: map[string]string{"SISF_COMMIT": string(req.Commit), "SISF_UNIT": req.Unit, "SISF_BRANCH": req.Branch},
	})
	if err != nil {
		return ImageResult{}, err
	}
	if result.TimedOut || result.ExitCode != 0 {
		return ImageResult{}, core.NewError(core.CodeUnavailable, "the sandboxed build did not finish successfully")
	}
	out := ImageResult{}
	scanner := bufio.NewScanner(bytes.NewReader(result.Stdout))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		value := func(prefix string) (string, bool) {
			v, ok := strings.CutPrefix(line, prefix)
			return strings.TrimSpace(v), ok
		}
		if v, ok := value(outImageDigest); ok {
			out.Digest = core.Digest(v)
		} else if v, ok := value(outImage); ok {
			out.Image = v
		} else if v, ok := value(outTests); ok {
			out.TestsPassed, out.TestEvidence = v == "passed", "sandbox build: tests "+v
		} else if v, ok := value(outSBOM); ok {
			out.SBOMRef = v
		} else if v, ok := value(outProvenance); ok {
			out.ProvenanceRef = v
		}
	}
	if out.Digest.Validate() != nil || out.Image == "" {
		return ImageResult{}, core.NewError(core.CodeUnavailable, "the sandboxed build reported no image and digest")
	}
	return out, nil
}

// untrustedRefused is the typed refusal to run untrusted code on a backend
// that cannot isolate it.
func untrustedRefused(backend string) *core.Error {
	return core.NewError(core.CodePermissionDenied,
		"the "+backend+" execution backend cannot isolate untrusted code; configure the docker execution backend")
}
