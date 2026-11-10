package control_test

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The dashboard marks the values it shows with data attributes, so a test can
// read them back without depending on the layout around them.
var (
	workboardRow = regexp.MustCompile(`<tr class="issue" data-issue-id="([^"]*)" data-state="([^"]*)">\s*<td><a href="[^"]*">[^<]*</a></td>\s*<td class="title">([^<]*)</td>\s*<td>[^<]*</td>\s*<td>(\d+)</td>`)
	countRow     = regexp.MustCompile(`<tr data-state-count="([^"]*)"><td>(?:<a [^>]*>)?[^<]*(?:</a>)?</td><td>(\d+)</td></tr>`)
	zoneRow      = regexp.MustCompile(`<tr data-zone="([^"]*)" data-status="([^"]*)">`)
	fieldCell    = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`data-field="` + name + `">([^<]*)<`)
	}
)

// shownRows is what the workboard page shows for each issue.
type shownRow struct {
	state, title string
	attempt      int
}

func shownRows(t *testing.T, page string) map[string]shownRow {
	t.Helper()
	rows := map[string]shownRow{}
	for _, m := range workboardRow.FindAllStringSubmatch(page, -1) {
		attempt, _ := strconv.Atoi(m[4])
		rows[html.UnescapeString(m[1])] = shownRow{state: html.UnescapeString(m[2]), title: html.UnescapeString(m[3]), attempt: attempt}
	}
	return rows
}

func shownField(t *testing.T, page, name string) string {
	t.Helper()
	m := fieldCell(name).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the page shows no %q field", name)
	}
	return html.UnescapeString(m[1])
}

// seedMixedBoard creates issues in different states with awkward titles.
func seedMixedBoard(t *testing.T, e *env) {
	t.Helper()
	titles := map[int]string{1: `Plain title`, 2: `Fish & chips <b>bold</b> "quoted"`, 3: `Ünïcode ✓ title`}
	e.seedRecord(t, 1, toReady...)
	e.db.Clock.Advance(time.Minute)
	e.seedRecord(t, 2, toDeveloping...)
	e.db.Clock.Advance(time.Minute)
	e.seedRecord(t, 3, core.EventInfoMissing)
	for n, title := range titles {
		e.deliver(t, issueEvent{delivery: "title-" + strconv.Itoa(n), action: "opened", number: n, title: title})
	}
}

func TestAPIAndDashboardShowTheSameWorkboard(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		seedMixedBoard(t, e)
		session := e.login(t)

		var apiList control.IssueList
		getJSON(t, e.api(http.MethodGet, "/api/v1/issues"), &apiList)
		shown := shownRows(t, e.page("/workboard", session).Body.String())

		if len(apiList.Issues) != 3 || len(shown) != len(apiList.Issues) {
			t.Fatalf("api shows %d issues, dashboard shows %d", len(apiList.Issues), len(shown))
		}
		for _, issue := range apiList.Issues {
			row, ok := shown[issue.ID]
			if !ok {
				t.Errorf("the dashboard lacks %s", issue.ID)
				continue
			}
			if row.state != string(issue.State) || row.title != issue.Title || row.attempt != issue.Attempt {
				t.Errorf("%s: dashboard %+v, api state=%s title=%q attempt=%d", issue.ID, row, issue.State, issue.Title, issue.Attempt)
			}
		}
	})
}

func TestAPIAndDashboardShowTheSameIssue(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		seedMixedBoard(t, e)
		session := e.login(t)
		for n := 1; n <= 3; n++ {
			id := issueRef(n).String()
			var apiIssue control.IssueDetail
			getJSON(t, e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape(id)), &apiIssue)
			rec := e.page("/issues/"+url.PathEscape(id), session)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s page = %d", id, rec.Code)
			}
			page := rec.Body.String()
			checks := map[string][2]string{
				"id":      {shownField(t, page, "id"), apiIssue.ID},
				"state":   {shownField(t, page, "state"), string(apiIssue.State)},
				"version": {shownField(t, page, "version"), strconv.FormatInt(apiIssue.Version, 10)},
				"attempt": {shownField(t, page, "attempt"), strconv.Itoa(apiIssue.Attempt)},
				"title":   {shownField(t, page, "title"), apiIssue.Title},
				"author":  {shownField(t, page, "author"), apiIssue.Author},
			}
			for field, pair := range checks {
				if pair[0] != pair[1] {
					t.Errorf("%s %s: dashboard %q, api %q", id, field, pair[0], pair[1])
				}
			}
		}
	})
}

func TestAPIAndDashboardShowTheSameOverview(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		seedMixedBoard(t, e)
		e.probes.set(core.ZoneAgent, func(context.Context) error { return errors.New("agent zone unreachable") })
		session := e.login(t)

		var status control.FactoryStatus
		getJSON(t, e.api(http.MethodGet, "/api/v1/factory/status"), &status)
		page := e.page("/", session).Body.String()

		counts := map[string]int{}
		for _, m := range countRow.FindAllStringSubmatch(page, -1) {
			counts[html.UnescapeString(m[1])], _ = strconv.Atoi(m[2])
		}
		if len(counts) != len(status.IssueCounts) {
			t.Fatalf("dashboard lists %d states, api %d", len(counts), len(status.IssueCounts))
		}
		for _, c := range status.IssueCounts {
			if counts[string(c.State)] != c.Count {
				t.Errorf("state %s: dashboard %d, api %d", c.State, counts[string(c.State)], c.Count)
			}
		}
		zones := map[string]string{}
		for _, m := range zoneRow.FindAllStringSubmatch(page, -1) {
			zones[m[1]] = m[2]
		}
		for _, z := range status.Zones {
			if zones[string(z.Zone)] != string(z.Status) {
				t.Errorf("zone %s: dashboard %q, api %q", z.Zone, zones[string(z.Zone)], z.Status)
			}
		}
		if got := shownField(t, page, "issue-total"); got != strconv.Itoa(status.IssueTotal) {
			t.Errorf("total: dashboard %s, api %d", got, status.IssueTotal)
		}
		if got := shownField(t, page, "pending-deliveries"); got != strconv.Itoa(status.PendingDeliveries) {
			t.Errorf("pending: dashboard %s, api %d", got, status.PendingDeliveries)
		}
	})
}

func TestFilteredWorkboardMatchesFilteredAPI(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		seedMixedBoard(t, e)
		session := e.login(t)
		for _, query := range []string{"state=Ready", "state=Developing&state=Clarifying", "state=Closed", "limit=2"} {
			var apiList control.IssueList
			getJSON(t, e.api(http.MethodGet, "/api/v1/issues?"+query), &apiList)
			shown := shownRows(t, e.page("/workboard?"+query, session).Body.String())
			if len(shown) != len(apiList.Issues) {
				t.Errorf("%s: dashboard %d rows, api %d", query, len(shown), len(apiList.Issues))
			}
			for _, issue := range apiList.Issues {
				if _, ok := shown[issue.ID]; !ok {
					t.Errorf("%s: dashboard lacks %s", query, issue.ID)
				}
			}
		}
	})
}

func TestBothSurfacesFollowALifecycleChange(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		seedMixedBoard(t, e)
		session := e.login(t)
		id := issueRef(1).String()
		read := func() (apiState, pageState string) {
			var d control.IssueDetail
			getJSON(t, e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape(id)), &d)
			return string(d.State), shownField(t, e.page("/issues/"+url.PathEscape(id), session).Body.String(), "state")
		}
		if a, p := read(); a != "Ready" || p != "Ready" {
			t.Fatalf("before: api %s, dashboard %s", a, p)
		}
		e.advance(t, 1, core.EventClaimed)
		if a, p := read(); a != "Claimed" || p != "Claimed" {
			t.Fatalf("after the claim: api %s, dashboard %s; both must follow the store", a, p)
		}
	})
}
