package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Measure returns the digest of the supervisor that is running. It reads the
// running thing itself and never a value someone handed to the process.
type Measure func() (core.Digest, error)

// MeasureExecutable returns the SHA-256 of the executable this process runs.
// On Linux it reads the kernel's handle to the running image, which stays
// correct even when the file on disk was replaced after the process started.
func MeasureExecutable() (core.Digest, error) {
	path := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		located, err := os.Executable()
		if err != nil {
			return "", newError(core.CodeUnavailable, "the supervisor cannot locate its own executable").WithCause(err)
		}
		path = located
	}
	return DigestFile(path)
}

// DigestFile returns the SHA-256 of the file at path as a digest.
func DigestFile(path string) (core.Digest, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", newError(core.CodeUnavailable, "%s cannot be opened to measure it", path).WithCause(err)
	}
	defer func() { _ = file.Close() }() // read-only: a failed close loses nothing
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", newError(core.CodeUnavailable, "%s cannot be read to measure it", path).WithCause(err)
	}
	return core.Digest("sha256:" + hex.EncodeToString(hash.Sum(nil))), nil
}

// selfCheck is the evidence of a startup check that passed.
type selfCheck struct {
	// release is the manifest whose signature verified.
	release signing.SignedManifest
	// measured is the digest of the running supervisor, equal to the one the
	// manifest pins.
	measured core.Digest
}

// checkSelf decides whether this supervisor is the one the Owner signed for.
// It verifies the release manifest under verifier, requires the manifest to
// be for this factory, measures the running executable and compares. Any
// failure is a refusal to run: a supervisor that cannot prove what it is
// must not hold release authority.
func checkSelf(ctx context.Context, cfg Config, verifier core.Verifier, measure Measure) (selfCheck, error) {
	signed, err := signing.ReadSignedManifest(cfg.ManifestPath)
	if err != nil {
		return selfCheck{}, err
	}
	release, err := signed.Verify(ctx, verifier)
	if err != nil {
		return selfCheck{}, err
	}
	if release.FactoryID != cfg.FactoryID {
		return selfCheck{}, newError(core.CodePermissionDenied,
			"the release manifest is signed for factory %q, not for this factory %q", release.FactoryID, cfg.FactoryID)
	}
	measured, err := measure()
	if err != nil {
		return selfCheck{}, err
	}
	if err := measured.Validate(); err != nil {
		return selfCheck{}, newError(core.CodeUnavailable, "the supervisor's measurement of itself is not a digest").WithCause(err)
	}
	if measured != release.SupervisorExecutable {
		return selfCheck{}, newError(core.CodeTampered,
			"this supervisor's digest %s is not the digest %s pinned by the signed release manifest", measured, release.SupervisorExecutable)
	}
	return selfCheck{release: signed, measured: measured}, nil
}
