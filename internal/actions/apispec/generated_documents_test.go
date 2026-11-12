package apispec_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/internal/actions/apispec"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	apiDir    = "../../../api"
	briefPath = "../../../docs/self-improvement-software-factory-decision-brief.en.md"
)

func TestGeneratedDocumentsAreFresh(t *testing.T) {
	t.Parallel()
	reg, err := apispec.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	openapi, llms, err := apispec.Render(reg.Specs())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{apispec.OpenAPIFile: openapi, apispec.LLMsFile: llms} {
		got, err := os.ReadFile(filepath.Join(apiDir, name))
		if err != nil {
			t.Fatalf("read api/%s: %v; run go generate ./internal/actions/...", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("api/%s is stale; run go generate ./internal/actions/... and commit the result", name)
		}
	}
}

func TestRenderingIsStableAndValidYAML(t *testing.T) {
	t.Parallel()
	reg, err := apispec.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := apispec.Render(reg.Specs())
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := apispec.Render(reg.Specs())
	if !bytes.Equal(first, second) {
		t.Fatal("rendering the same specs twice gave different bytes")
	}
	var doc struct {
		OpenAPI string                               `yaml:"openapi"`
		Paths   map[string]map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(first, &doc); err != nil {
		t.Fatalf("the OpenAPI document is not valid YAML: %v", err)
	}
	if doc.OpenAPI != "3.1.0" || len(doc.Paths) != len(reg.Specs()) {
		t.Fatalf("openapi %q with %d paths for %d actions", doc.OpenAPI, len(doc.Paths), len(reg.Specs()))
	}
	for _, spec := range reg.Specs() {
		op := doc.Paths[apispec.PathPrefix+spec.Name]["post"]
		if op["x-sisf-sensitivity"] != string(spec.Sensitivity) || op["x-sisf-dry-run"] != spec.Mutates() {
			t.Errorf("%s: operation %v does not carry the spec", spec.Name, op)
		}
	}
}

// queryListFromBrief reads the action names of the query list in the brief
// (the line that starts with "- Queries:") and the runs.explain action the
// agent experience requires, expanding "budget.get/set".
func queryListFromBrief(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "- Queries:") {
			line = l
		}
	}
	if line == "" || !strings.Contains(string(raw), "`runs.explain`") {
		t.Fatal("the brief no longer lists its queries where this test reads them")
	}
	names := []string{"runs.explain"}
	for _, m := range regexp.MustCompile("`([a-z_]+\\.[a-z_./]+)`").FindAllStringSubmatch(line, -1) {
		area, verbs, _ := strings.Cut(m[1], ".")
		for _, verb := range strings.Split(verbs, "/") {
			names = append(names, area+"."+verb)
		}
	}
	slices.Sort(names)
	return names
}

func TestEveryQueryOfTheBriefIsRegistered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		sensitivity core.Sensitivity
	}{
		{"approvals.decide", core.SensitivitySensitive},
		{"approvals.list", core.SensitivityRead},
		{"budget.get", core.SensitivityRead},
		{"budget.set", core.SensitivitySensitive},
		{"changelog.list", core.SensitivityRead},
		{"decisions.propose_update", core.SensitivityWrite},
		{"deployments.rollback", core.SensitivitySensitive},
		{"docs.list", core.SensitivityRead},
		{"factory.status", core.SensitivityRead},
		{"features.list", core.SensitivityRead},
		{"issues.list", core.SensitivityRead},
		{"runs.explain", core.SensitivityRead},
		{"runs.inspect", core.SensitivityRead},
		{"secrets.list_refs", core.SensitivityRead},
	}
	var table []string
	for _, tc := range cases {
		table = append(table, tc.name)
	}
	if fromBrief := queryListFromBrief(t); !slices.Equal(table, fromBrief) {
		t.Fatalf("the table %v differs from the brief's query list %v", table, fromBrief)
	}
	reg, err := apispec.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, ok := reg.Lookup(tc.name)
			if !ok {
				t.Fatal("not registered")
			}
			if action.Spec.Sensitivity != tc.sensitivity {
				t.Fatalf("sensitivity = %s, want %s", action.Spec.Sensitivity, tc.sensitivity)
			}
			if tc.sensitivity == core.SensitivitySensitive && !action.Spec.StepUp {
				t.Fatal("a sensitive action must demand step-up")
			}
		})
	}
}
