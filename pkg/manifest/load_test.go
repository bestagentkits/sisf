package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// examplePath is the Docker example every contributor sees first.
const examplePath = "../../.sisf/factory.yaml"

// exampleYAML returns the text of the example manifest.
func exampleYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("the example manifest must be readable: %v", err)
	}
	return string(data)
}

// example returns the parsed example manifest, fresh for every caller.
func example(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.ParseFile(examplePath)
	if err != nil {
		t.Fatalf("the example manifest must parse: %v", err)
	}
	return m
}

func TestParseDecodesAManifestWrittenInYAML(t *testing.T) {
	m := example(t)
	checks := map[string]bool{
		"identity":       m.FactoryID == "acme-hello" && m.DeploymentID == "local" && m.Mode == manifest.ModeDocker,
		"three zones":    len(m.Components.Missing()) == 0,
		"nine units":     len(m.Units) == 9 && m.Units[manifest.UnitQARunner].Lifecycle == manifest.LifecycleEphemeral,
		"four networks":  len(m.Networks) == 4 && m.Networks[manifest.NetworkSandbox].EgressVia == "edge",
		"supervisor":     m.Supervisor != nil && m.Supervisor.StartupSelfCheck && m.Supervisor.DigestChange.HumanApproved(),
		"volume owner":   m.Volumes["trust"].Zone == core.ZoneControl,
		"duration text":  m.Limits.ClarifyTimeout.Std() == 72*time.Hour && m.Release.Rollback.Window.Std() == 10*time.Minute,
		"mount":          m.Units[manifest.UnitSupervisor].Mounts[0].Volume == "trust",
		"model route":    m.ModelRoutes.Routes[core.StepQA].Provider == "openai",
		"egress default": m.Egress.Mode == core.EgressAllowlist && m.Egress.Validate() == nil,
		"QA credentials": m.Units[manifest.UnitQARunner].CredentialsRef == "local:zone2-qa" && m.Units[manifest.UnitRunner].CredentialsRef == "",
		"volume holder":  m.Volumes["artifacts"].Holder == manifest.UnitArtifacts && m.Volumes["state-data"].Holder == "",
		"one route off":  !m.ModelRoutes.SameModelRoute,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s was not decoded from the YAML file", name)
		}
	}
}

func TestParseRejectsWhatItCannotAcceptAsWritten(t *testing.T) {
	valid := exampleYAML(t)
	cases := map[string]struct {
		yaml string
		want string
	}{
		"a key the manifest does not have": {strings.Replace(valid, "mode: docker", "mode: docker\nreplicas: 3", 1), "replicas"},
		"a misspelled nested key":          {strings.Replace(valid, "trust_volume: trust", "trust_volum: trust", 1), "trust_volum"},
		"a value of the wrong type":        {strings.Replace(valid, "port: 7461", "port: eighty", 1), "eighty"},
		"a duration that is not a span":    {strings.Replace(valid, "run_timeout: 30m", "run_timeout: soon", 1), "duration"},
		"a negative duration":              {strings.Replace(valid, "run_timeout: 30m", "run_timeout: -5m", 1), "duration"},
		"a repeated key":                   {valid + "\nmode: standalone\n", "already defined"},
		"a second document":                {valid + "\n---\nmode: standalone\n", "exactly one"},
		"an empty file":                    {"", "empty"},
		"text that is not YAML":            {"units: [unclosed", "decoded"},
		"a file beyond the size limit":     {valid + strings.Repeat("# padding\n", manifest.MaxSize/10+1), "larger"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.yaml != "" && tc.yaml == valid {
				t.Fatal("the case did not change the example")
			}
			m, err := manifest.Parse([]byte(tc.yaml))
			if err == nil || m != nil {
				t.Fatalf("Parse accepted %s", name)
			}
			if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want INVALID_ARGUMENT mentioning %q", err, tc.want)
			}
		})
	}
}

func TestParseFileTellsAMissingFileFromAnUnreadableOne(t *testing.T) {
	dir := t.TempDir()
	if _, err := manifest.ParseFile(filepath.Join(dir, "absent.yaml")); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("a missing file must be NOT_FOUND, got %v", err)
	}
	if _, err := manifest.ParseFile(dir); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("a directory cannot be read as a manifest, got %v", err)
	}
	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("mode: [docker"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := manifest.ParseFile(broken)
	if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), broken) {
		t.Errorf("a parse error must name the file, got %v", err)
	}
}

func TestLoadReturnsOnlyAManifestThatMatchesTheSchema(t *testing.T) {
	if _, err := manifest.Load(examplePath); err != nil {
		t.Fatalf("the example must load: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "factory.yaml")
	text := strings.Replace(exampleYAML(t), "port: 7461", "port: 70000", 1)
	if err := os.WriteFile(bad, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(bad)
	if m != nil || !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("Load = %v, %v; want INVALID_ARGUMENT and no manifest", m, err)
	}
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Details["units.app.port"] == "" {
		t.Fatalf("the error must locate the violation, got details %v", typed)
	}
	if !strings.Contains(err.Error(), "units.app.port") {
		t.Fatalf("the message must name the offending key, got %v", err)
	}
}

func TestParseReportsProblemsInTheWordsOfTheFile(t *testing.T) {
	valid := exampleYAML(t)
	cases := map[string]struct {
		yaml string
		want string
	}{
		"an unknown key":       {strings.Replace(valid, "mode: docker", "mode: docker\nreplicas: 3", 1), `unknown key "replicas"`},
		"text for a number":    {strings.Replace(valid, "port: 7461", "port: eighty", 1), "text (eighty) does not fit here"},
		"a number for a table": {strings.Replace(valid, "mode: docker", "mode: docker\nextensions: 3", 1), "an integer (3) does not fit here"},
		"a list for a name":    {strings.Replace(valid, "mode: docker", "mode: [docker]", 1), "a list does not fit here"},
		"a span that is none":  {strings.Replace(valid, "run_timeout: 30m", "run_timeout: soon", 1), `"soon": duration must be`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("Parse accepted the input")
			}
			message := err.Error()
			if !strings.Contains(message, tc.want) || !strings.Contains(message, "line ") {
				t.Errorf("error = %q, want it to name the line and say %q", message, tc.want)
			}
			// The reader edits a YAML file and has never seen the decoder's types.
			if strings.Count(message, string(core.CodeInvalidArgument)) != 1 {
				t.Errorf("error %q must state its code exactly once", message)
			}
			for _, leak := range []string{"manifest.", "unmarshal", "!!", "map["} {
				if strings.Contains(message, leak) {
					t.Errorf("error %q exposes decoder detail %q", message, leak)
				}
			}
		})
	}
}

// The acknowledgement of a single model route is false unless written.
func TestParseReadsTheSameModelRouteAcknowledgement(t *testing.T) {
	t.Parallel()
	text := strings.Replace(exampleYAML(t), "  max_escalations: 2\n", "  max_escalations: 2\n  same_model_route: true\n", 1)
	m, err := manifest.Parse([]byte(text))
	if err != nil || !m.ModelRoutes.SameModelRoute {
		t.Fatalf("same_model_route: true must be read, got %v, %v", m, err)
	}
	if got := manifest.Validate(m); len(got) != 0 {
		t.Fatalf("the acknowledgement is part of the schema, got %v", got)
	}
	if _, err := manifest.Parse([]byte(strings.Replace(text, "\n  same_model_route: true\n", "\n  same_model_route: maybe\n", 1))); err == nil {
		t.Fatal("an acknowledgement that is not true or false must be refused")
	}
}
