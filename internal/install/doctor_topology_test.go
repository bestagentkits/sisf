package install

import (
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Each way the release topology can be wider or weaker than it must be
// fails the doctor's check of it.
func TestTheDoctorFailsEveryBreachOfTheReleaseTopology(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		check  string
		change func(*dockerHost)
	}{
		"another container on the docker network": {"docker filters", func(h *dockerHost) { h.dockerNetwork += " sisf-hello-app-dispatcher" }},
		"a filter is missing":                     {"docker filters", func(h *dockerHost) { h.dockerNetwork = "sisf-hello-app-supervisor" }},
		"the dispatcher reaches a filter":         {"docker filters", func(h *dockerHost) { h.dispatcherReach = true }},
		"the supervisor cannot reach a filter":    {"docker filters", func(h *dockerHost) { h.supervisorBlocks = true }},
		"the edge admin is published":             {"edge admin", func(h *dockerHost) { h.edgePorts += "7469/tcp=127.0.0.1:7469 " }},
		"the edge is published on every address":  {"edge admin", func(h *dockerHost) { h.edgePorts = "7460/tcp=0.0.0.0:7460 " }},
		"the edge admin takes no token":           {"edge admin", func(h *dockerHost) { h.adminRefusal = "" }},
		"a runner image is gone":                  {"runner images", func(h *dockerHost) { h.missingImage = true }},
		"a delegated key is gone":                 {"delegated keys", func(h *dockerHost) { delete(h.trust, "delegation-deploy_artifact.key") }},
		"the delegations are gone":                {"delegated keys", func(h *dockerHost) { h.trust = nil }},
		"the sandbox network is not internal":     {"sandbox network", func(h *dockerHost) { h.sandboxInternal = "false" }},
		"the sandbox network is gone":             {"sandbox network", func(h *dockerHost) { h.sandboxInternal = "" }},
	} {
		r := runDoctor(t, dockerFactoryOn(t, dockerPS, supervisorJSON, tc.change))
		if v := verdictOf(r, tc.check); len(v) != 1 || v[0] != VerdictFail {
			t.Errorf("%s: %s = %v (%s)", name, tc.check, v, detailOf(r, tc.check))
		}
	}
}

func TestTheDoctorReportsDelegatedKeysThatExpireOrHaveExpired(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		after time.Duration
		want  Verdict
	}{
		"fresh":         {time.Hour, VerdictOK},
		"expiring soon": {delegation.MaxLifetime - time.Hour, VerdictWarn},
		"expired":       {delegation.MaxLifetime + time.Hour, VerdictFail},
	} {
		in := builtStandalone(t, healthyNet(), nil)
		clock := sqlstoretest.NewClock(fixedStart)
		clock.Advance(tc.after)
		in.Clock = clock
		r := runDoctor(t, in)
		if v := verdictOf(r, "delegated keys"); len(v) != 1 || v[0] != tc.want {
			t.Errorf("%s: delegated keys = %v, want %s (%s)", name, v, tc.want, detailOf(r, "delegated keys"))
		}
	}
}

func TestTheDoctorSaysAProductionDockerInstallReleasesNothing(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeDocker)
	production(&o)
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	// The Compose backend runs images, and the supervisor accepts no build
	// on an image it cannot observe: the stages stay off, and the doctor
	// says why, whether or not the manifest still names a launcher and a
	// build command, and never asks for either.
	for _, tc := range []struct {
		name  string
		extra map[string]string
	}{
		{"as init writes it", nil},
		{"naming a launcher and a build command", map[string]string{"qa_agent_binary": "/opt/sisf/qa-launcher", "build_command": "make candidate-image"}},
	} {
		name := tc.name
		for k, v := range tc.extra {
			m.Extensions[k] = v
		}
		var c collector
		in.checkStages(&c, m)
		if v := verdictOf(c.report, "build evidence"); len(v) != 1 || v[0] != VerdictWarn || !strings.Contains(detailOf(c.report, "build evidence"), "cannot observe") {
			t.Fatalf("%s: build evidence = %v %q", name, v, detailOf(c.report, "build evidence"))
		}
		for _, check := range []string{"lifecycle", "qa launcher", "build command"} {
			if v := verdictOf(c.report, check); len(v) != 0 {
				t.Fatalf("%s: %s = %v %q", name, check, v, detailOf(c.report, check))
			}
		}
	}
	// Where the stages run, the launcher is trusted on the operator's word,
	// and the doctor says so.
	m.Mode = manifest.ModeStandalone
	BuildSandbox{Endpoint: "http://127.0.0.1:17480", Network: "sisf-shop-sandbox", EgressProxy: "http://egress:3128",
		RunnerImage: "127.0.0.1:17481/sisf-runner@sha256:" + strings.Repeat("ab", 32)}.extensions(m.Extensions)
	var c collector
	in.checkStages(&c, m)
	if v := verdictOf(c.report, "qa launcher"); len(v) != 1 || v[0] != VerdictWarn ||
		!strings.Contains(detailOf(c.report, "qa launcher"), "/opt/sisf/qa-launcher") || !strings.Contains(detailOf(c.report, "qa launcher"), "on your word") {
		t.Fatalf("with a launcher, qa launcher = %v %q", v, detailOf(c.report, "qa launcher"))
	}
}

func TestTheStandaloneEdgeAdminMustRefuseACallerWithoutItsToken(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet().with("127.0.0.1:7469/", reply{code: 200}), nil)
	if v := verdictOf(runDoctor(t, in), "edge admin"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("edge admin = %v", v)
	}
}
