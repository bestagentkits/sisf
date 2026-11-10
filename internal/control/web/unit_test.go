package web

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestLocalPathRefusesEveryOffSiteTarget(t *testing.T) {
	t.Parallel()
	for target, want := range map[string]string{
		"/workboard?state=Ready": "/workboard?state=Ready", "": "/", "workboard": "/", "https://evil.test/": "/", "//evil.test": "/",
		"/\\evil.test": "/", "/a\r\nSet-Cookie: x=1": "/",
	} {
		if got := localPath(target); got != want {
			t.Errorf("localPath(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestTableFromListsWhatTheActionReturned(t *testing.T) {
	t.Parallel()
	got := tableFrom(json.RawMessage(`{"count":2,"features":[{"name":"a","n":1,"tags":["x"]},{"name":"b","extra":true}]}`))
	if want := []string{"extra", "n", "name", "tags"}; !reflect.DeepEqual(got.Cols, want) {
		t.Errorf("columns = %v, want %v", got.Cols, want)
	}
	if want := [][]string{{"", "1", "a", `["x"]`}, {"true", "", "b", ""}}; !reflect.DeepEqual(got.Rows, want) {
		t.Errorf("rows = %v, want %v", got.Rows, want)
	}
	for _, raw := range []string{``, `null`, `{}`, `{"a":1}`, `[1,2]`, `not json`} {
		if tab := tableFrom(json.RawMessage(raw)); len(tab.Rows) != 0 {
			t.Errorf("tableFrom(%q) invented rows %v", raw, tab.Rows)
		}
	}
}

func TestAgeUsesTheLargestWholeUnit(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{-time.Second: "just now", 30 * time.Second: "under a minute", time.Minute: "1 minute",
		90 * time.Minute: "1 hour", 5 * time.Hour: "5 hours", 72 * time.Hour: "3 days"} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestMoneyShowsMillionths(t *testing.T) {
	t.Parallel()
	if got := money(5_000_000); got != "5.000000" {
		t.Errorf("money = %q", got)
	}
	if got := money(1_234_567); got != "1.234567" {
		t.Errorf("money = %q", got)
	}
}

func TestNextLinkPointsOnlyAtDashboardPages(t *testing.T) {
	t.Parallel()
	if got := nextLink(coreNext("approvals.get", map[string]string{"id": "a/b c"})); got != "/approvals/a%2Fb%20c" {
		t.Errorf("approval link = %q", got)
	}
	if got := nextLink(coreNext("deployments.rollback", nil)); got != "" {
		t.Errorf("an action without a page got the link %q", got)
	}
}
