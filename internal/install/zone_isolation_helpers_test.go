package install

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
	"gopkg.in/yaml.v3"
)

// Helpers of the static zone isolation checks.

const fakeID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testImages() Images {
	return Images{Factoryd: fakeID, Supervisor: fakeID, Edge: fakeID, App: fakeID, Sisf: fakeID}
}

// renderCompose renders the project of a fresh Docker install.
func renderCompose(t *testing.T, mutate func(*Options)) (*Installer, []byte) {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeDocker)
	if mutate != nil {
		mutate(&o)
	}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o, _ = o.normalize()
	data, err := newComposeData(in.Layout, o, testImages(), []string{"sisf", "state", "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	return in, out
}

// edited decodes the Compose file, lets the test break one rule, and
// re-encodes it.
func edited(t *testing.T, data []byte, edit func(services, root map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	edit(root["services"].(map[string]any), root)
	out, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func violationRules(vs []Violation) string {
	var rules []string
	for _, v := range vs {
		rules = append(rules, v.Rule+"@"+v.Service)
	}
	return strings.Join(rules, " ")
}

func composeFileOf(t *testing.T, data []byte) composeFile {
	t.Helper()
	var f composeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
