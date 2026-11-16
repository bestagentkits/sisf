package supplychain

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	observedProgram = core.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	otherProgram    = core.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	builtCommit     = core.CommitSHA("1111111111111111111111111111111111111111")
	otherCommit     = core.CommitSHA("2222222222222222222222222222222222222222")
)

func observation(digest core.Digest, commit core.CommitSHA) BuildObservation {
	started := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	return BuildObservation{
		ImageName: "registry.local/app", Digest: digest, Commit: commit, SourceRepo: "github.com/acme/app",
		BuilderID: "sisf-supervisor/sandbox:docker", InvocationID: "run-1/1", StartedOn: started, FinishedOn: started.Add(time.Minute),
		Unread: "the program carries no Go build information",
	}
}

func documentsOf(t *testing.T, o BuildObservation) BuildDocuments {
	t.Helper()
	d, err := MakeBuildDocuments(o)
	if err != nil {
		t.Fatalf("MakeBuildDocuments: %v", err)
	}
	return d
}

// The documents of a build are made from what the supervisor observed and
// check out against the program and commit they were made for, and against
// nothing else.
func TestTheSupervisorsBuildDocumentsHoldOnlyForTheBuildTheyDescribe(t *testing.T) {
	t.Parallel()
	d := documentsOf(t, observation(observedProgram, builtCommit))
	if err := CheckBuildDocuments(observedProgram, builtCommit, d); err != nil {
		t.Fatalf("the documents of the build: %v", err)
	}
	other := documentsOf(t, observation(otherProgram, builtCommit))
	withAnotherSBOM := observation(observedProgram, builtCommit)
	withAnotherSBOM.Unread = "another reason, so another SBOM"
	recordsAnotherSBOM := documentsOf(t, withAnotherSBOM)
	for name, c := range map[string]struct {
		digest core.Digest
		commit core.CommitSHA
		docs   BuildDocuments
	}{
		"another program":                    {otherProgram, builtCommit, d},
		"another commit":                     {observedProgram, otherCommit, d},
		"bare strings for references":        {observedProgram, builtCommit, BuildDocuments{SBOMRef: "sbom:invented", ProvenanceRef: "provenance:invented"}},
		"references without their documents": {observedProgram, builtCommit, BuildDocuments{SBOMRef: d.SBOMRef, ProvenanceRef: d.ProvenanceRef}},
		"an SBOM edited after it was made": {observedProgram, builtCommit, func() BuildDocuments {
			e := d
			e.SBOM = []byte(strings.Replace(string(d.SBOM), "registry.local/app", "evil.local/app", 1))
			return e
		}()},
		"the SBOM of another program, its reference updated": {observedProgram, builtCommit, func() BuildDocuments {
			e := d
			e.SBOM, e.SBOMRef = other.SBOM, other.SBOMRef
			return e
		}()},
		"the provenance of another program": {observedProgram, builtCommit, func() BuildDocuments {
			e := d
			e.Provenance, e.ProvenanceRef = other.Provenance, other.ProvenanceRef
			return e
		}()},
		"a provenance that records another SBOM": {observedProgram, builtCommit, func() BuildDocuments {
			e := d
			e.Provenance, e.ProvenanceRef = recordsAnotherSBOM.Provenance, recordsAnotherSBOM.ProvenanceRef
			return e
		}()},
	} {
		if err := CheckBuildDocuments(c.digest, c.commit, c.docs); err == nil {
			t.Errorf("%s: the documents were accepted", name)
		}
	}
}

// What the supervisor could not read is never listed as known: an SBOM made
// without the program's build information says so, and one made with it
// lists its modules.
func TestTheSBOMListsOnlyModulesTheSupervisorRead(t *testing.T) {
	t.Parallel()
	o := observation(observedProgram, builtCommit)
	o.Unread = ""
	if _, err := MakeBuildDocuments(o); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an SBOM without build information or a reason: %v, want INVALID_ARGUMENT", err)
	}

	unread := documentsOf(t, observation(observedProgram, builtCommit))
	var bom cdxBOM
	if err := json.Unmarshal(unread.SBOM, &bom); err != nil {
		t.Fatal(err)
	}
	if len(bom.Components) != 0 || !hasProperty(bom.Metadata.Component, propModules, "unknown: the program carries no Go build information") {
		t.Errorf("an SBOM without build information lists %d components and %v", len(bom.Components), bom.Metadata.Component.Properties)
	}
	if why, unknown := ModulesUnknown(unread.SBOM); !unknown || why != "the program carries no Go build information" {
		t.Errorf("ModulesUnknown of an SBOM without build information = %q, %v", why, unknown)
	}
	if why, unknown := ModulesUnknown([]byte("{}")); !unknown || why == "" {
		t.Errorf("an SBOM that does not say where its modules came from counts as listing them: %q, %v", why, unknown)
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("the test binary carries no build information")
	}
	o = observation(observedProgram, builtCommit)
	o.Program = info
	read := documentsOf(t, o)
	if err := CheckBuildDocuments(observedProgram, builtCommit, read); err != nil {
		t.Fatalf("the documents of a program with build information: %v", err)
	}
	bom = cdxBOM{}
	if err := json.Unmarshal(read.SBOM, &bom); err != nil {
		t.Fatal(err)
	}
	if len(bom.Components) == 0 || !hasProperty(bom.Metadata.Component, propModules, modulesFromBinary) {
		t.Errorf("an SBOM from build information lists %d components and %v", len(bom.Components), bom.Metadata.Component.Properties)
	}
	if why, unknown := ModulesUnknown(read.SBOM); unknown {
		t.Errorf("the modules of an SBOM from build information count as unknown: %q", why)
	}
}

func hasProperty(c cdxComponent, name, value string) bool {
	for _, p := range c.Properties {
		if p.Name == name && p.Value == value {
			return true
		}
	}
	return false
}
