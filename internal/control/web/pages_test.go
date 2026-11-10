package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// inert lists fragments that would be live markup if escaping failed.
var inert = []string{"<script>alert", "<img src=x", `"></a><script`, "</a><script"}

func TestHostileTextFromIssuesAndCatalogRendersInert(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	b := e.browser(t)
	b.signIn(t, ownerSub)
	// Make the approval request carry hostile text in its diff and risk summary.
	plan := b.post(t, "/actions/factory.emergency_stop", stopForm("x"))
	m := approvalLink.FindStringSubmatch(plan.body)
	if m == nil {
		t.Fatalf("no approval request was filed: %.300s", plan.body)
	}
	hostileIssue := url.QueryEscape(`"><script>alert(3)</script>`)
	for _, path := range []string{"/", "/blocked?issue=acme/app%231", "/runs?issue=acme/app%231", "/runs?issue=" + hostileIssue,
		"/blocked?issue=" + hostileIssue, "/product", "/security", "/setup", "/approvals", "/approvals/" + m[1],
		"/telemetry?q=" + hostileIssue, "/workboard?state=" + hostileIssue} {
		r := b.get(t, path)
		for _, live := range append(inert, `"><script>alert(3)`) {
			if strings.Contains(r.body, live) {
				t.Errorf("GET %s renders attacker markup %q", path, live)
			}
		}
	}
	if r := b.get(t, "/blocked?issue=acme/app%231"); !strings.Contains(r.body, "&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt;") {
		t.Errorf("the hostile reason is not shown as escaped text: %.600s", r.body)
	}
	if r := b.get(t, "/approvals/"+m[1]); !strings.Contains(r.body, "factory/stop:&lt;script&gt;") {
		t.Errorf("the hostile diff is not shown as escaped text")
	}
}

func TestBlockedPageExplainsWithEvidenceAndNextSteps(t *testing.T) {
	t.Parallel()
	f := defaultFixtures()
	f.hostile = "The QA gate failed twice."
	b := newEnv(t, f).browser(t)
	b.signIn(t, ownerSub)
	r := b.get(t, "/blocked?issue=acme/app%231")
	for _, want := range []string{`data-cause="blocked"`, "The QA gate failed twice.", `data-next-action="approvals.list"`, `href="/approvals"`,
		`data-next-action="runs.inspect"`, `href="/runs?issue=acme%2Fapp%231"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("blocked page lacks %q: %.800s", want, r.body)
		}
	}
}

func TestWorkboardAndRunGraphShowRecordsFromTheActions(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	board := b.get(t, "/workboard")
	for _, want := range []string{`data-issue="acme/app#1"`, `data-issue="acme/app#2"`, "3 hours", "10 minutes", `href="/blocked?issue=acme%2fapp%231"`} {
		if !strings.Contains(board.body, want) {
			t.Errorf("workboard lacks %q", want)
		}
	}
	graph := b.get(t, "/runs?issue=acme/app%231")
	if !strings.Contains(graph.body, `data-state="Blocked" aria-current="step"`) || !strings.Contains(graph.body, `data-field="state">Blocked`) {
		t.Errorf("run graph does not mark the current state: %.600s", graph.body)
	}
	if over := b.get(t, "/"); !strings.Contains(over.body, `data-field="open-approvals">1<`) || !strings.Contains(over.body, `data-field="budget-mode">unlimited`) {
		t.Errorf("overview lacks the status and budget figures")
	}
}

func TestSetupWizardChecksEachStepBeforeTheSampleIssue(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		fix     *fixtures
		pass    bool
		missing string
	}{
		"all checks pass":       {fix: defaultFixtures(), pass: true},
		"budget not confirmed":  {fix: &fixtures{hostile: "h", githubRef: true, sample: true}, missing: `data-step="budget" data-status="fail"`},
		"no GitHub App":         {fix: &fixtures{hostile: "h", budgetConfirmed: true, sample: true}, missing: `data-step="github-app" data-status="fail"`},
		"no sample issue offer": {fix: &fixtures{hostile: "h", budgetConfirmed: true, githubRef: true}, missing: "offers no sample-issue action"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, tc.fix)
			b := e.browser(t)
			b.signIn(t, ownerSub)
			page := b.get(t, "/setup")
			if page.status != http.StatusOK || !strings.Contains(page.body, `data-step="sso" data-status="pass"`) {
				t.Fatalf("setup = %d: %.400s", page.status, page.body)
			}
			if tc.pass {
				if strings.Contains(page.body, " disabled") || !strings.Contains(page.body, `action="/actions/setup.sample_issue"`) {
					t.Fatalf("the sample issue is not offered after all checks pass")
				}
				if r := b.post(t, "/actions/setup.sample_issue", url.Values{}); r.status != http.StatusOK {
					t.Errorf("sample issue = %d: %.300s", r.status, r.body)
				}
				if n := e.seen.ran("dashboard"); n != 1 {
					t.Errorf("the sample issue ran %d times", n)
				}
				return
			}
			if !strings.Contains(page.body, tc.missing) {
				t.Errorf("setup lacks %q", tc.missing)
			}
			if tc.fix.sample && !strings.Contains(page.body, " disabled") {
				t.Errorf("the sample issue can be started while a check fails")
			}
		})
	}
}
