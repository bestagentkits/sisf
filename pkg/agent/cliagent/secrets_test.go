package cliagent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
)

// A token in the environment or in the tool's output reaches no event, no
// status, no artifact and no error.
func TestATokenInTheEnvironmentOrTheOutputAppearsNowhereWeRecord(t *testing.T) {
	t.Parallel()
	const (
		envToken    = "opaque-provider-credential-7731"
		outputToken = "sk-ant-api03-OUTPUTTOKENVALUE0099"
		ghToken     = "ghp_abcdefghijklmnopqrstuvwxyz0123"
	)
	r := newRig(t, clitest.Script{Steps: []clitest.Step{
		{EchoEnv: "PROVIDER_ACCESS_TOKEN"},
		{Emit: `{"type":"msg","text":"calling the API with ` + outputToken + ` and ` + envToken + `"}`},
		{Emit: `{"type":"msg","text":"GITHUB_TOKEN=` + ghToken + `"}`},
		{Stderr: "auth failed for " + envToken + " " + outputToken},
		{Emit: `{"type":"fail","detail":"401 for ` + envToken + ` ` + outputToken + `"}`},
	}}, "PROVIDER_ACCESS_TOKEN="+envToken)
	ref := r.start(t, newTask("run-0001", "k1"))
	status := r.wait(t, ref)

	var seen []string
	seen = append(seen, status.Detail)
	for _, e := range r.events(t, ref) {
		seen = append(seen, string(e.Payload))
	}
	info, err := r.b.RunInfo(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, info.ToolVersion, info.ModelRoute.String())
	for _, text := range seen {
		for _, secret := range []string{envToken, outputToken, ghToken} {
			if strings.Contains(text, secret) {
				t.Errorf("secret %q leaked into %q", secret, text)
			}
		}
	}
	if !strings.Contains(status.Detail, "401") {
		t.Errorf("redaction should keep the useful part of the detail, got %q", status.Detail)
	}
}

func TestTokensStayOutOfCollectedArtifacts(t *testing.T) {
	t.Parallel()
	const token = "sk-ant-api03-ARTIFACTTOKEN000042"
	r := newRig(t, clitest.Script{Steps: []clitest.Step{
		{Emit: `{"type":"msg","text":"used ` + token + `"}`},
		{Stderr: "debug " + token},
		{Emit: `{"type":"done"}`},
	}})
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	artifacts, err := r.b.CollectArtifacts(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[agent.ArtifactKind]bool{}
	for _, a := range artifacts {
		kinds[a.Kind] = true
		data, err := os.ReadFile(strings.TrimPrefix(a.URI, "file://"))
		if err != nil {
			t.Fatalf("read %s: %v", a.Name, err)
		}
		if strings.Contains(string(data), token) {
			t.Errorf("artifact %s holds the token", a.Name)
		}
		if int64(len(data)) != a.Size {
			t.Errorf("artifact %s size %d, content %d", a.Name, a.Size, len(data))
		}
	}
	if !kinds[agent.ArtifactTranscript] || !kinds[agent.ArtifactLog] {
		t.Errorf("want a transcript and a log, got %v", kinds)
	}
	info, err := os.Stat(filepath.Dir(strings.TrimPrefix(artifacts[0].URI, "file://")))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("artifact directory must be private: %v %v", info, err)
	}
}
