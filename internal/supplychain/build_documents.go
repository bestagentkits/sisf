package supplychain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime/debug"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// BuildObservation is what the supervisor itself observed of one build: the
// program it received and hashed, the commit it asked to be built, and its
// own clock. The SBOM and provenance of a build are made from it alone,
// never from what a build command reports.
type BuildObservation struct {
	// ImageName names the build without its digest, for example
	// "registry.local/app".
	ImageName string
	// Digest is the sha256 of the program the supervisor received.
	Digest     core.Digest
	Commit     core.CommitSHA
	SourceRepo string
	// BuilderID names the supervisor's builder.
	BuilderID string
	// InvocationID names this build run.
	InvocationID string
	StartedOn    time.Time
	FinishedOn   time.Time
	// Program is the Go build information read from the received program;
	// nil when there is none to read.
	Program *debug.BuildInfo
	// Unread says why Program is nil. The SBOM records it: modules the
	// supervisor could not read are never listed as if they were known.
	Unread string
}

// BuildDocuments are the SBOM and provenance the supervisor made of one
// build, and their references: the sha256 of each document's bytes.
type BuildDocuments struct {
	SBOM          []byte
	SBOMRef       string
	Provenance    []byte
	ProvenanceRef string
}

// sisf properties of the SBOM's described component.
const (
	propModules       = "sisf:modules"
	modulesFromBinary = "the Go build information of the program the supervisor received"
)

// MakeBuildDocuments makes the SBOM and provenance of a build from what the
// supervisor observed of it. The SBOM lists the modules of the received
// program's Go build information, or none and why, and the provenance
// names the program's digest, the commit and the SBOM.
func MakeBuildDocuments(o BuildObservation) (BuildDocuments, error) {
	if o.Program == nil && strings.TrimSpace(o.Unread) == "" {
		return BuildDocuments{}, invalid("an SBOM without the program's build information must say why")
	}
	note := modulesFromBinary
	var infos []*debug.BuildInfo
	if o.Program != nil {
		if o.Program.Main.Path == "" {
			return BuildDocuments{}, invalid("the program's build information names no main module")
		}
		infos = []*debug.BuildInfo{o.Program}
	} else {
		note = "unknown: " + o.Unread
	}
	sbom, err := sbomOf(o.ImageName, o.Digest, o.FinishedOn, infos, []cdxProperty{{Name: propModules, Value: note}})
	if err != nil {
		return BuildDocuments{}, err
	}
	sbomRef := digestOf(sbom)
	statement, err := NewProvenance(ProvenanceInput{
		ArtifactName: o.ImageName, ArtifactDigest: o.Digest, SourceRepo: o.SourceRepo, CommitSHA: o.Commit,
		BuilderID: o.BuilderID, SBOMDigest: sbomRef, InvocationID: o.InvocationID, StartedOn: o.StartedOn, FinishedOn: o.FinishedOn,
	})
	if err != nil {
		return BuildDocuments{}, err
	}
	provenance, err := json.Marshal(statement)
	if err != nil {
		return BuildDocuments{}, core.NewError(core.CodeInternal, "the provenance cannot be encoded").WithCause(err)
	}
	return BuildDocuments{SBOM: sbom, SBOMRef: string(sbomRef), Provenance: provenance, ProvenanceRef: string(digestOf(provenance))}, nil
}

// CheckBuildDocuments returns nil only when d are documents of the build of
// commit whose program has digest: each reference is the sha256 of its
// document, the SBOM describes that program, and the provenance covers it,
// names commit and records that SBOM. A reference that names no document
// held with it is a claim, and is refused.
func CheckBuildDocuments(digest core.Digest, commit core.CommitSHA, d BuildDocuments) error {
	if len(d.SBOM) == 0 || len(d.Provenance) == 0 {
		return invalid("the build holds no SBOM or no provenance document")
	}
	if string(digestOf(d.SBOM)) != d.SBOMRef || string(digestOf(d.Provenance)) != d.ProvenanceRef {
		return core.NewError(core.CodeTampered, "the build's SBOM or provenance reference is not the digest of the document it holds")
	}
	var bom cdxBOM
	if err := json.Unmarshal(d.SBOM, &bom); err != nil {
		return invalid("the build's SBOM is not a CycloneDX document").WithCause(err)
	}
	described := bom.Metadata.Component
	if bom.BOMFormat != cycloneDXFormat || described.Version != string(digest) || !sameHashes(described.Hashes, digestHashes(digest)) {
		return invalid("the build's SBOM does not describe the program %s", digest)
	}
	var statement Statement
	if err := json.Unmarshal(d.Provenance, &statement); err != nil {
		return invalid("the build's provenance is not an in-toto statement").WithCause(err)
	}
	if !statement.Covers(digest) {
		return invalid("the build's provenance does not cover the program %s", digest)
	}
	if statement.Predicate.BuildDefinition.ExternalParameters["commit"] != string(commit) {
		return invalid("the build's provenance names another commit than %s", commit)
	}
	sbomRef := core.Digest(d.SBOMRef)
	for _, b := range statement.Predicate.RunDetails.Byproducts {
		if len(b.Digest) == 1 && b.Digest["sha256"] != "" && "sha256:"+b.Digest["sha256"] == string(sbomRef) {
			return nil
		}
	}
	return invalid("the build's provenance does not record its SBOM")
}

// ModulesUnknown reports whether an SBOM lists no module the supervisor
// read, and why. An SBOM that does not say where its modules came from
// counts as one whose modules are unknown, never as a full list.
func ModulesUnknown(sbom []byte) (why string, unknown bool) {
	var bom cdxBOM
	if err := json.Unmarshal(sbom, &bom); err != nil {
		return "the SBOM cannot be read", true
	}
	for _, p := range bom.Metadata.Component.Properties {
		if p.Name != propModules {
			continue
		}
		if p.Value == modulesFromBinary {
			return "", false
		}
		if why, ok := strings.CutPrefix(p.Value, "unknown: "); ok && why != "" {
			return why, true
		}
	}
	return "the SBOM does not say where its modules came from", true
}

// digestOf is the sha256 digest of data.
func digestOf(data []byte) core.Digest {
	sum := sha256.Sum256(data)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func sameHashes(a, b []cdxHash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
