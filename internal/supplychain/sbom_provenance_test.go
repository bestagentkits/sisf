package supplychain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// moduleSum returns a well-formed go.sum hash for name and its hex form.
func moduleSum(name string) (h1, hexHash string) {
	sum := sha256.Sum256([]byte(name))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:]), hex.EncodeToString(sum[:])
}

// goVersionM renders `go version -m` output for two binaries of one image.
func goVersionM() string {
	yaml, _ := moduleSum("yaml")
	pgx, _ := moduleSum("pgx")
	fork, _ := moduleSum("fork")
	return strings.Join([]string{
		"/usr/local/bin/sisf: go1.25.14",
		"\tpath\tgithub.com/bestagentkits/sisf/cmd/sisf",
		"\tmod\tgithub.com/bestagentkits/sisf\t(devel)\t",
		"\tdep\tgopkg.in/yaml.v3\tv3.0.1\t" + yaml,
		"\tdep\tgithub.com/jackc/pgx/v5\tv5.12.0\t" + pgx,
		"\tdep\texample.com/upstream\tv1.0.0\t",
		"\t=>\texample.com/fork\tv1.0.1\t" + fork,
		"\tbuild\tCGO_ENABLED=0",
		"\tbuild\tvcs.revision=" + string(headSHA),
		"/usr/local/bin/supervisor: go1.25.14",
		"\tpath\tgithub.com/bestagentkits/sisf/cmd/supervisor",
		"\tmod\tgithub.com/bestagentkits/sisf\t(devel)\t",
		"\tdep\tgopkg.in/yaml.v3\tv3.0.1\t" + yaml,
		"",
	}, "\n")
}

func sbomInput() SBOMInput {
	return SBOMInput{ImageName: "ghcr.io/acme/app", ImageDigest: imageDigest, GoVersionM: goVersionM(), Timestamp: start}
}

func TestParseGoVersionMReadsEveryBinary(t *testing.T) {
	t.Parallel()
	infos, err := ParseGoVersionM(goVersionM())
	if err != nil {
		t.Fatalf("ParseGoVersionM: %v", err)
	}
	if len(infos) != 2 || infos[0].Path != "github.com/bestagentkits/sisf/cmd/sisf" || len(infos[0].Deps) != 3 || infos[1].GoVersion != "go1.25.14" {
		t.Fatalf("parsed %d binaries: %+v", len(infos), infos)
	}
	for name, output := range map[string]string{
		"empty":              "",
		"no binary line":     "\tpath\tx\n",
		"garbage":            "hello world\n",
		"no main module":     "/bin/x: go1.25.14\n\tpath\tx\n",
		"bad build line":     "/bin/x: go1.25.14\n\tpath\tx\n\tmod\tx\t(devel)\t\n\tdep\tonly-a-path\n",
		"oversized":          strings.Repeat("x", maxGoVersionOutput+1),
		"info before binary": "\tmod\tx\tv1.0.0\t\n/bin/x: go1.25.14\n",
	} {
		if _, err := ParseGoVersionM(output); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestSBOMIsCycloneDXOfTheImageAndItsModules(t *testing.T) {
	t.Parallel()
	out, err := GenerateSBOM(sbomInput())
	if err != nil {
		t.Fatalf("GenerateSBOM: %v", err)
	}
	var doc cdxBOM
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("SBOM is not JSON: %v", err)
	}
	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != cycloneDXVersion || !strings.HasPrefix(doc.SerialNumber, "urn:uuid:") {
		t.Fatalf("header = %+v", doc)
	}
	image := doc.Metadata.Component
	if image.Type != "container" || image.Version != string(imageDigest) || len(image.Hashes) != 1 ||
		image.Hashes[0].Alg != "SHA-256" || image.Hashes[0].Content != strings.TrimPrefix(string(imageDigest), "sha256:") {
		t.Fatalf("image component = %+v", image)
	}
	byPURL := map[string]cdxComponent{}
	for _, c := range doc.Components {
		byPURL[c.PURL] = c
	}
	_, yamlHex := moduleSum("yaml")
	if c := byPURL["pkg:golang/gopkg.in/yaml.v3@v3.0.1"]; c.Type != "library" || len(c.Hashes) != 1 || c.Hashes[0].Content != yamlHex {
		t.Errorf("yaml component = %+v", c)
	}
	if c, ok := byPURL["pkg:golang/example.com/fork@v1.0.1"]; !ok || len(c.Properties) != 1 || c.Properties[0].Value != "example.com/upstream@v1.0.0" {
		t.Errorf("a replaced module is not described as its replacement: %+v", c)
	}
	if _, ok := byPURL["pkg:golang/example.com/upstream@v1.0.0"]; ok {
		t.Error("the replaced module itself is listed as linked")
	}
	if c := byPURL["pkg:golang/github.com/bestagentkits/sisf@(devel)"]; c.Type != "application" {
		t.Errorf("main module = %+v", c)
	}
	if len(doc.Components) != 4 {
		t.Errorf("components = %d, want 4 with the shared module once", len(doc.Components))
	}
	if len(doc.Dependencies) == 0 || doc.Dependencies[0].Ref != image.BOMRef {
		t.Errorf("dependencies = %+v", doc.Dependencies)
	}

	again, err := GenerateSBOM(sbomInput())
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("the same input produced another SBOM")
	}
	other := sbomInput()
	other.ImageDigest = otherDigest
	changed, err := GenerateSBOM(other)
	if err != nil || bytes.Equal(out, changed) {
		t.Fatal("another image produced the same SBOM")
	}
}

func TestSBOMRefusesIncompleteInput(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*SBOMInput){
		"no image name":     func(in *SBOMInput) { in.ImageName = "" },
		"name with digest":  func(in *SBOMInput) { in.ImageName = "ghcr.io/acme/app@" + string(imageDigest) },
		"image by tag only": func(in *SBOMInput) { in.ImageDigest = "latest" },
		"no timestamp":      func(in *SBOMInput) { in.Timestamp = time.Time{} },
		"no binaries":       func(in *SBOMInput) { in.GoVersionM = "" },
	}
	for name, change := range cases {
		in := sbomInput()
		change(&in)
		if _, err := GenerateSBOM(in); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func provenanceInput(t *testing.T) ProvenanceInput {
	t.Helper()
	sbom, err := GenerateSBOM(sbomInput())
	if err != nil {
		t.Fatalf("GenerateSBOM: %v", err)
	}
	return ProvenanceInput{
		ArtifactName: "ghcr.io/acme/app", ArtifactDigest: imageDigest,
		SourceRepo: "https://github.com/acme/app", CommitSHA: headSHA,
		BuilderID: "https://github.com/bestagentkits/sisf/supervisor", SBOMDigest: DigestOf(sbom),
		InvocationID: "run-0001", StartedOn: start, FinishedOn: start.Add(3 * time.Minute),
	}
}

func TestProvenanceDescribesTheBuild(t *testing.T) {
	t.Parallel()
	in := provenanceInput(t)
	stmt, err := NewProvenance(in)
	if err != nil {
		t.Fatalf("NewProvenance: %v", err)
	}
	if !stmt.Covers(imageDigest) || stmt.Covers(otherDigest) {
		t.Fatal("Covers does not match the subject digest exactly")
	}
	encoded, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"_type":"` + StatementType, `"predicateType":"` + ProvenancePredicate, `"gitCommit":"` + string(headSHA), strings.TrimPrefix(string(in.SBOMDigest), "sha256:")} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("statement lacks %s", want)
		}
	}
	forged := stmt
	forged.PredicateType = "https://example.com/other"
	if forged.Covers(imageDigest) {
		t.Error("a statement of another predicate covers the artifact")
	}
}

func TestProvenanceRefusesGaps(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ProvenanceInput){
		"no artifact digest": func(in *ProvenanceInput) { in.ArtifactDigest = "" },
		"no SBOM":            func(in *ProvenanceInput) { in.SBOMDigest = "" },
		"branch not commit":  func(in *ProvenanceInput) { in.CommitSHA = "main" },
		"no repository":      func(in *ProvenanceInput) { in.SourceRepo = "" },
		"no builder":         func(in *ProvenanceInput) { in.BuilderID = "" },
		"no invocation":      func(in *ProvenanceInput) { in.InvocationID = "" },
		"finish before start": func(in *ProvenanceInput) {
			in.FinishedOn = in.StartedOn.Add(-time.Second)
		},
	}
	for name, change := range cases {
		in := provenanceInput(t)
		change(&in)
		if _, err := NewProvenance(in); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
