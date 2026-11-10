package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// parityRequests are the requests the parity test sends for each action, as
// the Owner without a fresh step-up. Writes the dashboard has no form for
// are dry runs; the dashboard's forms run for real on every surface.
func parityRequests(t *testing.T, f *harness.Factory) map[string]call {
	t.Helper()
	issue := `{"issue":"` + f.Issue(1).String() + `"}`
	decisions, err := os.ReadFile(filepath.Join("..", "..", policy.PathDecisions))
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ := json.Marshal(map[string]string{"content": string(decisions) + "\nA note added by the parity check.\n", "summary": "parity check"})
	return map[string]call{
		"factory.status":           {Input: raw(`{}`)},
		"issues.list":              {Input: raw(`{}`)},
		"runs.inspect":             {Input: raw(issue)},
		"runs.explain":             {Input: raw(issue)},
		"approvals.list":           {Input: raw(`{}`)},
		"approvals.get":            {Input: raw(`{"id":"apr-unknown"}`)},
		"approvals.decide":         {Input: raw(`{"request_id":"apr-unknown","nonce":"n","subject_digest":"sha256:00","policy_revision":"` + string(f.PolicyRevision) + `","decision":"approve"}`)},
		"factory.emergency_stop":   {Input: raw(`{"reason":"parity check"}`)},
		"deployments.rollback":     {Input: raw(`{"reason":"parity check"}`)},
		"telemetry.search":         {Input: raw(`{"query":"parity"}`)},
		"setup.sample_issue":       {Input: raw(`{}`)},
		"budget.get":               {Input: raw(`{}`)},
		"budget.set":               {Input: raw(`{"mode":"unlimited"}`), DryRun: true},
		"secrets.list_refs":        {Input: raw(`{}`)},
		"features.list":            {Input: raw(`{}`)},
		"changelog.list":           {Input: raw(`{}`)},
		"docs.list":                {Input: raw(`{}`)},
		"decisions.propose_update": {Input: proposal, DryRun: true},
	}
}

// volatile are output fields that differ on every real run of the same
// request: the sample issue is a new issue each time.
var volatile = map[string][]string{"setup.sample_issue": {"issue"}}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// auditShape is what one call left in the audit log, without what differs
// by surface: the surface itself and the session or token handle.
type auditShape struct {
	Action, Outcome, Phase, Code, Actor string
}

// SISF-13: every action, called the same way by the same person, gets the
// same authorization decision, the same typed result or error code and the
// same audit record on the CLI, REST, MCP, the WebMCP tool path and the
// dashboard, and every surface lists exactly the registered actions.
func TestEveryActionAnswersTheSameOnEverySurface(t *testing.T) {
	f := harness.New(t, harness.Options{})
	f.Supervisor.SetLKG("web", core.Digest("sha256:"+string(bytes.Repeat([]byte("ab"), 32))))
	owner := f.SignInOwner()
	c := newClients(t, f, owner)
	// Let the step-up go stale: no token carries one, so the session must
	// not either for the same request to mean the same thing everywhere.
	f.Clock.Advance(6 * time.Minute)

	requests := parityRequests(t, f)
	specs := f.Control.Actions.Specs()
	for _, spec := range specs {
		req, ok := requests[spec.Name]
		if !ok {
			t.Errorf("the parity test sends nothing for %s; add a request for it", spec.Name)
			continue
		}
		surfaces := []string{surfaceCLI, surfaceREST, surfaceMCP, surfaceWebMCP}
		if slices.Contains(dashboardForms, spec.Name) {
			surfaces = append(surfaces, surfaceDashboard)
		}
		var first outcome
		var firstShape []auditShape
		for i, surface := range surfaces {
			before := auditSeq(t, f)
			got := mask(c.on(t, surface, spec.Name, req), volatile[spec.Name])
			shape, surfacesSeen := auditSince(t, f, before)
			for _, s := range surfacesSeen {
				if s != auditSurface[surface] {
					t.Errorf("%s on %s was audited as surface %q", spec.Name, surface, s)
				}
			}
			if i == 0 {
				first, firstShape = got, shape
				t.Logf("%s: %q on %d surfaces, %d audit records", spec.Name, got.Code, len(surfaces), len(shape))
				continue
			}
			if got.Code != first.Code {
				t.Errorf("%s: %s answered %q, %s answered %q (%s)", spec.Name, surfaces[0], first.Code, surface, got.Code, got.Message)
			}
			if !bytes.Equal(got.Output, first.Output) || (surface != surfaceDashboard && !bytes.Equal(got.Plan, first.Plan)) {
				t.Errorf("%s: %s and %s returned different results:\n%s\n%s", spec.Name, surfaces[0], surface, first.Output, got.Output)
			}
			if !slices.Equal(shape, firstShape) {
				t.Errorf("%s: %s audited %+v, %s audited %+v", spec.Name, surfaces[0], firstShape, surface, shape)
			}
		}
	}
	for name := range requests {
		if !slices.ContainsFunc(specs, func(s core.ActionSpec) bool { return s.Name == name }) {
			t.Errorf("the parity test calls %s, which is not registered", name)
		}
	}
	assertSurfaceListsAreTheRegistry(t, f, c, specs)
}

// mask blanks the volatile fields of an output.
func mask(o outcome, fields []string) outcome {
	if len(fields) == 0 || o.Output == nil {
		return o
	}
	var m map[string]any
	if json.Unmarshal(o.Output, &m) != nil {
		return o
	}
	for _, f := range fields {
		if _, ok := m[f]; ok {
			m[f] = "<varies>"
		}
	}
	o.Output, _ = json.Marshal(m)
	return o
}

func auditSeq(t *testing.T, f *harness.Factory) int64 {
	t.Helper()
	events, err := f.Control.Audit.Events(context.Background(), 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

// auditSince returns the shape of each audit record after seq and the
// surfaces they name.
func auditSince(t *testing.T, f *harness.Factory, seq int64) ([]auditShape, []core.Surface) {
	t.Helper()
	events, err := f.Control.Audit.Events(context.Background(), seq, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var shapes []auditShape
	var surfaces []core.Surface
	for _, e := range events {
		shapes = append(shapes, auditShape{Action: e.Action, Outcome: string(e.Outcome), Phase: e.Details["phase"],
			Code: e.Details["code"], Actor: e.ActorID})
		surfaces = append(surfaces, e.Surface)
	}
	return shapes, surfaces
}

// openAPIOperation finds the action paths of the published OpenAPI document.
var openAPIOperation = regexp.MustCompile(`(?m)^  /api/v1/actions/([a-z0-9_.]+):`)

// assertSurfaceListsAreTheRegistry compares what each surface offers with
// the registry: MCP's tool list, the WebMCP tool list, the published OpenAPI
// document REST and the CLI serve, and the dashboard's forms.
func assertSurfaceListsAreTheRegistry(t *testing.T, f *harness.Factory, c *clients, specs []core.ActionSpec) {
	t.Helper()
	var want []string
	for _, s := range specs {
		want = append(want, s.Name)
	}
	slices.Sort(want)
	tools, err := c.mcp.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var mcpNames, webNames []string
	for _, tool := range tools.Tools {
		mcpNames = append(mcpNames, tool.Name)
	}
	for name := range c.tools {
		webNames = append(webNames, name)
	}
	doc, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var restNames []string
	for _, m := range openAPIOperation.FindAllSubmatch(doc, -1) {
		restNames = append(restNames, string(m[1]))
	}
	for surface, names := range map[string][]string{"MCP": mcpNames, "WebMCP": webNames, "REST and CLI (OpenAPI)": restNames} {
		slices.Sort(names)
		if !slices.Equal(names, want) {
			t.Errorf("%s lists %v, the registry %v", surface, names, want)
		}
	}
	for _, name := range dashboardForms {
		if !slices.Contains(want, name) {
			t.Errorf("the dashboard posts %s, which is not registered", name)
		}
	}
}
