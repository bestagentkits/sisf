package conformance

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestTheCodingStepAndTheQAStepEachHaveAModelRoute(t *testing.T) {
	t.Parallel()
	for _, kind := range []core.StepKind{core.StepCoding, core.StepQA} {
		for mode, valid := range bothModes {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				delete(m.ModelRoutes.Routes, kind)
				report := Check(m)
				wantOnly(t, report, RuleQARouteMissing, "model_routes.routes."+string(kind))
				if len(report.Findings) != 1 {
					t.Fatalf("one missing route is one finding, got %+v", report.Findings)
				}
			})
		}
	}
	// Other kinds of step do not stand in for the two that must differ.
	m := docker(t)
	m.ModelRoutes.Routes = map[core.StepKind]core.ModelRoute{core.StepIntake: {Provider: "anthropic", Model: "small"}}
	report := Check(m)
	wantOnly(t, report, RuleQARouteMissing, "model_routes.routes.coding")
	wantOnly(t, report, RuleQARouteMissing, "model_routes.routes.qa")
}

func TestQARunsOnAnotherProviderOrModelThanTheCodingAgent(t *testing.T) {
	t.Parallel()
	coding := core.ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"}
	cases := []struct {
		name   string
		change func(r *manifest.ModelRoutes)
		path   string
	}{
		{"QA on the coding route",
			func(r *manifest.ModelRoutes) { r.Routes[core.StepQA] = coding }, "model_routes.routes.qa"},
		{"QA on the coding model at more effort",
			func(r *manifest.ModelRoutes) {
				r.Routes[core.StepQA] = core.ModelRoute{Provider: "anthropic", Model: "mid", Effort: "high"}
			},
			"model_routes.routes.qa"},
		{"QA on the coding model written in another case",
			func(r *manifest.ModelRoutes) {
				r.Routes[core.StepQA] = core.ModelRoute{Provider: "Anthropic", Model: " MID "}
			},
			"model_routes.routes.qa"},
		{"the coding agent escalates onto the QA model",
			func(r *manifest.ModelRoutes) {
				r.Escalation = map[core.StepKind][]core.ModelRoute{core.StepCoding: {{Provider: "anthropic", Model: "large"}, r.Routes[core.StepQA]}}
			}, "model_routes.escalation.coding[1]"},
		{"QA escalates onto the coding model",
			func(r *manifest.ModelRoutes) {
				r.Escalation = map[core.StepKind][]core.ModelRoute{core.StepQA: {coding}}
			}, "model_routes.escalation.qa[0]"},
	}
	for _, tc := range cases {
		for mode, valid := range bothModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				m.ModelRoutes.Routes[core.StepCoding] = coding
				tc.change(&m.ModelRoutes)
				report := Check(m)
				wantOnly(t, report, RuleQASameModelRoute, tc.path)
				if len(report.Findings) != 1 || report.SameModelRoute {
					t.Fatalf("one shared route is one finding and no acknowledgement was given, got %+v", report)
				}
				if !strings.Contains(report.Findings[0].Fix, "same_model_route: true") || !strings.Contains(report.Findings[0].Message, "reduced assurance") {
					t.Errorf("the finding must name the acknowledgement and what it gives up, got %+v", report.Findings[0])
				}
			})
		}
	}

	// Another model of the same provider, or the same model name at another
	// provider, is another route.
	for _, other := range []core.ModelRoute{{Provider: "anthropic", Model: "large"}, {Provider: "openai", Model: "mid"}} {
		m := docker(t)
		m.ModelRoutes.Routes[core.StepCoding], m.ModelRoutes.Routes[core.StepQA] = coding, other
		if report := Check(m); !report.Conformant() || report.SameModelRoute {
			t.Errorf("QA on %s differs from coding on %s, got %+v", other, coding, report)
		}
	}
}

// A deployment with a single model route says so. The acknowledgement adds
// no independence; the report carries the reduced assurance.
func TestASingleModelRouteNeedsTheOwnersAcknowledgement(t *testing.T) {
	t.Parallel()
	for mode, valid := range bothModes {
		m := valid(t)
		if m.ModelRoutes.SameModelRoute {
			t.Fatalf("%s: same_model_route must default to false", mode)
		}
		// An acknowledgement nothing relies on changes nothing.
		m.ModelRoutes.SameModelRoute = true
		if report := Check(m); !report.Conformant() || report.SameModelRoute {
			t.Fatalf("%s: routes that differ need no acknowledgement, got %+v", mode, report)
		}
		m.ModelRoutes.Routes[core.StepQA] = m.ModelRoutes.Routes[core.StepCoding]
		report := Check(m)
		if !report.Conformant() || !report.SameModelRoute {
			t.Fatalf("%s: with the acknowledgement the manifest is conformant at reduced assurance, got %+v", mode, report)
		}
		if !slices.ContainsFunc(report.Notes(), func(note string) bool {
			return strings.HasPrefix(note, "assurance: reduced, same model route") && strings.Contains(note, "same_model_route")
		}) {
			t.Errorf("%s: the reduced assurance must be shown beside the verdict, got %q", mode, report.Notes())
		}
	}
}

func TestTheRunnerAndTheQARunnerShareNoWorkspace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		valid  func(*testing.T) *manifest.Manifest
		change func(m *manifest.Manifest)
		path   string
	}{
		{"the qa-runner mounts the coder's workspace", docker,
			func(m *manifest.Manifest) { m.Units[manifest.UnitQARunner].Mounts[0].Volume = "workspaces" }, "units.qa-runner.mounts[0]"},
		{"the qa-runner mounts the coder's workspace beside its own", docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitQARunner, "workspaces", true) }, "units.qa-runner.mounts[1]"},
		{"the runner mounts the QA workspace", docker,
			func(m *manifest.Manifest) { m.Units[manifest.UnitRunner].Mounts[0].Volume = "qa-workspaces" }, "units.qa-runner.mounts[0]"},
		{"the runner reads the QA workspace", standalone,
			func(m *manifest.Manifest) { mount(m, manifest.UnitRunner, "qa-workspaces", true) }, "units.qa-runner.mounts[0]"},
		{"on one machine the QA workspace lies inside the coder's", standalone,
			func(m *manifest.Manifest) { remount(m, manifest.UnitQARunner, "data/workspaces/qa") }, "units.qa-runner.mounts[0]"},
		{"on one machine the coder's workspace lies inside the QA workspace", standalone,
			func(m *manifest.Manifest) { remount(m, manifest.UnitRunner, "data/qa-workspaces/run") }, "units.qa-runner.mounts[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tc.valid(t)
			tc.change(m)
			report := Check(m)
			wantOnly(t, report, RuleQASharedWorkspace, tc.path)
			if len(report.Findings) != 1 {
				t.Fatalf("one shared workspace is one finding, got %+v", report.Findings)
			}
		})
	}
}

func TestTheQARunnerHasCredentialsDifferentFromTheRunners(t *testing.T) {
	t.Parallel()
	const secret = "hunter2"
	cases := map[string]func(m *manifest.Manifest){
		"the qa-runner names none, so both use the zone's": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.CredentialsRef = "" })
		},
		"the qa-runner names the zone's, which the runner uses": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Agent.CredentialsRef })
		},
		"both name the same set of their own": func(m *manifest.Manifest) {
			for _, unit := range []manifest.UnitName{manifest.UnitRunner, manifest.UnitQARunner} {
				editUnit(m, unit, func(u *manifest.Unit) { u.CredentialsRef = "local:" + secret })
			}
		},
		"both name one set in different letter case": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitRunner, func(u *manifest.Unit) { u.CredentialsRef = "local:Zone2-QA" })
		},
	}
	for name, change := range cases {
		for mode, valid := range bothModes {
			t.Run(name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				change(m)
				report := Check(m)
				wantOnly(t, report, RuleQASharedCredentials, "units.qa-runner.credentials_ref")
				if len(report.Findings) != 1 {
					t.Fatalf("one shared credential set is one finding, got %+v", report.Findings)
				}
				if finding := report.Findings[0]; strings.Contains(finding.Message+finding.Fix, secret) {
					t.Errorf("the finding repeats the reference: %+v", finding)
				}
			})
		}
	}

	// What must differ is what each unit runs with, however it is named.
	m := docker(t)
	editUnit(m, manifest.UnitRunner, func(u *manifest.Unit) { u.CredentialsRef = "local:zone2-runner" })
	editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.CredentialsRef = "" })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a runner with credentials of its own leaves the zone's to the qa-runner, got %+v", report.Findings)
	}
	// Credentials that are not a reference are refused by the schema as well.
	m = docker(t)
	editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.CredentialsRef = "tok_" + secret })
	report := Check(m)
	wantOnly(t, report, RuleSchema, "units.qa-runner.credentials_ref")
	if strings.Contains(report.Findings[0].Message, secret) {
		t.Errorf("the finding repeats the value: %+v", report.Findings[0])
	}
}
