package web_test

import (
	"strings"
	"testing"
)

// A confirm button is offered only for a provider that can force a fresh
// sign-in of the person signed in: an Owner signed in through Google sees
// Google's and never GitHub's, and an Owner signed in through GitHub, whom
// no provider can confirm, sees one plain sentence and no button.
func TestConfirmButtonsAreOfferedOnlyThroughProvidersThatCanConfirmThePerson(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	google := e.browser(t)
	google.signIn(t, ownerSub)
	m := approvalLink.FindStringSubmatch(planStop(t, google, "runaway spend").body)
	if m == nil {
		t.Fatal("no approval request was filed")
	}
	page := google.get(t, "/approvals/"+m[1])
	if !strings.Contains(page.body, `action="/auth/step-up/google`) || strings.Contains(page.body, "/auth/step-up/github") ||
		strings.Contains(page.body, `data-state="no-step-up"`) {
		t.Fatalf("approval page for a Google Owner: %.1200s", page.body)
	}
	refused := google.post(t, "/actions/factory.emergency_stop", stopForm("again"))
	if !strings.Contains(refused.body, `action="/auth/step-up/google`) || strings.Contains(refused.body, "/auth/step-up/github") {
		t.Fatalf("step-up result page for a Google Owner: %.1200s", refused.body)
	}

	github := e.browser(t)
	github.signInWith(t, "github", ownerSub)
	for name, r := range map[string]reply{
		"approval page": github.get(t, "/approvals/"+m[1]),
		"result page":   github.post(t, "/actions/factory.emergency_stop", stopForm("from github")),
	} {
		if strings.Contains(r.body, "/auth/step-up/") || !strings.Contains(r.body, `data-state="no-step-up"`) ||
			!strings.Contains(r.body, "OpenID Connect") {
			t.Errorf("%s for a GitHub Owner (%d): %.1200s", name, r.status, r.body)
		}
	}
}
