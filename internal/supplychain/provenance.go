package supplychain

import (
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// In-toto statement and SLSA provenance identifiers.
const (
	StatementType        = "https://in-toto.io/Statement/v1"
	ProvenancePredicate  = "https://slsa.dev/provenance/v1"
	ProvenanceBuildType  = "https://github.com/bestagentkits/sisf/build/v1"
	provenanceGitSubject = "git+"
)

// ProvenanceInput describes one build.
type ProvenanceInput struct {
	// ArtifactName and ArtifactDigest are the subject: what was built.
	ArtifactName   string
	ArtifactDigest core.Digest
	// SourceRepo is the repository URL the build checked out.
	SourceRepo string
	CommitSHA  core.CommitSHA
	// BuilderID identifies the trusted builder, for example the supervisor.
	BuilderID string
	// SBOMDigest is the digest of the build's SBOM, recorded as a byproduct.
	SBOMDigest core.Digest
	// InvocationID identifies this build run.
	InvocationID string
	StartedOn    time.Time
	FinishedOn   time.Time
}

// Statement is an in-toto statement carrying SLSA v1 provenance.
type Statement struct {
	Type          string         `json:"_type"`
	Subject       []Subject      `json:"subject"`
	PredicateType string         `json:"predicateType"`
	Predicate     SLSAProvenance `json:"predicate"`
}

// Subject names one artifact by its digests.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// SLSAProvenance is the SLSA v1 provenance predicate.
type SLSAProvenance struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// BuildDefinition says what was built from what.
type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   map[string]string    `json:"externalParameters"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies"`
}

// ResourceDescriptor names one input or output by URI and digest.
type ResourceDescriptor struct {
	URI    string            `json:"uri,omitempty"`
	Name   string            `json:"name,omitempty"`
	Digest map[string]string `json:"digest"`
}

// RunDetails says who built it and when.
type RunDetails struct {
	Builder    Builder              `json:"builder"`
	Metadata   RunMetadata          `json:"metadata"`
	Byproducts []ResourceDescriptor `json:"byproducts,omitempty"`
}

// Builder identifies the trusted builder.
type Builder struct {
	ID string `json:"id"`
}

// RunMetadata times one build run.
type RunMetadata struct {
	InvocationID string    `json:"invocationId"`
	StartedOn    time.Time `json:"startedOn"`
	FinishedOn   time.Time `json:"finishedOn"`
}

// NewProvenance builds the provenance statement of one build. Every input is
// required: provenance with a gap proves nothing.
func NewProvenance(in ProvenanceInput) (Statement, error) {
	for _, d := range []core.Digest{in.ArtifactDigest, in.SBOMDigest} {
		if err := d.Validate(); err != nil {
			return Statement{}, err
		}
	}
	if err := in.CommitSHA.Validate(); err != nil {
		return Statement{}, err
	}
	switch {
	case strings.TrimSpace(in.ArtifactName) == "" || strings.TrimSpace(in.SourceRepo) == "":
		return Statement{}, invalid("provenance needs the artifact name and the source repository")
	case strings.TrimSpace(in.BuilderID) == "" || strings.TrimSpace(in.InvocationID) == "":
		return Statement{}, invalid("provenance needs the builder and the build invocation")
	case in.StartedOn.IsZero() || in.FinishedOn.Before(in.StartedOn):
		return Statement{}, invalid("provenance needs a start and a finish that does not precede it")
	}
	return Statement{
		Type:          StatementType,
		Subject:       []Subject{{Name: in.ArtifactName, Digest: digestMap(in.ArtifactDigest)}},
		PredicateType: ProvenancePredicate,
		Predicate: SLSAProvenance{
			BuildDefinition: BuildDefinition{
				BuildType:          ProvenanceBuildType,
				ExternalParameters: map[string]string{"repository": in.SourceRepo, "commit": string(in.CommitSHA)},
				ResolvedDependencies: []ResourceDescriptor{{
					URI:    provenanceGitSubject + in.SourceRepo + "@" + string(in.CommitSHA),
					Digest: map[string]string{"gitCommit": string(in.CommitSHA)},
				}},
			},
			RunDetails: RunDetails{
				Builder:    Builder{ID: in.BuilderID},
				Metadata:   RunMetadata{InvocationID: in.InvocationID, StartedOn: in.StartedOn.UTC(), FinishedOn: in.FinishedOn.UTC()},
				Byproducts: []ResourceDescriptor{{Name: "sbom.cdx.json", Digest: digestMap(in.SBOMDigest)}},
			},
		},
	}, nil
}

// Covers reports whether the statement is SLSA provenance whose single
// subject is exactly digest.
func (s Statement) Covers(digest core.Digest) bool {
	if s.Type != StatementType || s.PredicateType != ProvenancePredicate || len(s.Subject) != 1 {
		return false
	}
	want := digestMap(digest)
	got := s.Subject[0].Digest
	if len(got) != len(want) {
		return false
	}
	for alg, value := range want {
		if got[alg] != value {
			return false
		}
	}
	return true
}

// digestMap renders "alg:value" as the in-toto digest set {alg: value}.
func digestMap(d core.Digest) map[string]string {
	alg, value, _ := strings.Cut(string(d), ":")
	return map[string]string{alg: value}
}
