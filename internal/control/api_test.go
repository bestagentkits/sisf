package control_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestStatusReportsEveryLifecycleStateAndTheInboxBacklog(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		e.seedRecord(t, 2, toReady...)
		e.seedRecord(t, 3, toDeveloping...)
		e.deliver(t, issueEvent{delivery: "d-1", action: "opened", number: 1, title: "one"})

		var status control.FactoryStatus
		rec := e.api(http.MethodGet, "/api/v1/factory/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		getJSON(t, rec, &status)

		if status.FactoryID != e.cfg.FactoryID || !status.Healthy || status.IssueTotal != 3 || status.PendingDeliveries != 1 {
			t.Errorf("status = %+v", status)
		}
		if len(status.IssueCounts) != len(core.States()) {
			t.Fatalf("%d state counts, want one per lifecycle state", len(status.IssueCounts))
		}
		counts := map[core.State]int{}
		for _, c := range status.IssueCounts {
			counts[c.State] = c.Count
		}
		if counts[core.StateReady] != 2 || counts[core.StateDeveloping] != 1 || counts[core.StateClosed] != 0 {
			t.Errorf("counts = %v", counts)
		}
		if got := status.GeneratedAt; !got.Equal(e.db.Clock.Now()) {
			t.Errorf("generated_at = %v, want the fake clock %v", got, e.db.Clock.Now())
		}
	})
}

func TestIssueListFiltersByStateAndLimit(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		e.db.Clock.Advance(time.Minute)
		e.seedRecord(t, 2, toDeveloping...)
		e.db.Clock.Advance(time.Minute)
		e.seedRecord(t, 3, toReady...)

		list := func(query string) control.IssueList {
			t.Helper()
			rec := e.api(http.MethodGet, "/api/v1/issues?"+query)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d: %s", query, rec.Code, rec.Body.String())
			}
			var out control.IssueList
			getJSON(t, rec, &out)
			return out
		}
		all := list("")
		if all.Total != 3 || len(all.Issues) != 3 || all.Issues[0].ID != "acme/app#3" {
			t.Errorf("all = %+v, want 3 issues newest first", all)
		}
		ready := list("state=Ready")
		if ready.Total != 2 || ready.Issues[0].ID != "acme/app#3" || ready.Issues[1].ID != "acme/app#1" {
			t.Errorf("ready = %+v", ready)
		}
		both := list("state=Ready&state=Developing&state=Ready")
		if both.Total != 3 {
			t.Errorf("two states = %+v", both)
		}
		limited := list("limit=1")
		if limited.Total != 3 || len(limited.Issues) != 1 {
			t.Errorf("limited = %+v, want total 3 with one row", limited)
		}
		if none := list("state=Closed"); none.Total != 0 || none.Issues == nil {
			t.Errorf("closed = %+v, want an empty, non-null list", none)
		}
	})
}

func TestIssueListRejectsBadQueriesWithNextActions(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for _, query := range []string{"state=Shipped", "state=ready", "limit=0", "limit=-3", "limit=abc", "limit=100000"} {
			rec := e.api(http.MethodGet, "/api/v1/issues?"+query)
			apiErr := wantAPIError(t, rec, http.StatusBadRequest, core.CodeInvalidArgument)
			if len(apiErr.NextActions) == 0 {
				t.Errorf("%s: the error names no next action", query)
			}
		}
	})
}

func TestIssueDetailAndItsErrors(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 7, toDeveloping...)
		e.deliver(t, issueEvent{delivery: "d-7", action: "opened", number: 7, title: "Fix the thing", labels: []string{"kind:bug"}})

		var detail control.IssueDetail
		rec := e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape("acme/app#7"))
		if rec.Code != http.StatusOK {
			t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
		}
		getJSON(t, rec, &detail)
		if detail.ID != "acme/app#7" || detail.State != core.StateDeveloping || detail.Title != "Fix the thing" ||
			detail.Author != "alice" || len(detail.Labels) != 1 || detail.Version != 5 {
			t.Errorf("detail = %+v", detail)
		}

		missing := wantAPIError(t, e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape("acme/app#99")), http.StatusNotFound, core.CodeNotFound)
		if len(missing.NextActions) == 0 || missing.NextActions[0].Action != "issues.list" {
			t.Errorf("not-found next actions = %+v, want issues.list", missing.NextActions)
		}
		for _, id := range []string{"nonsense", "acme/app", "acme/app#0", "acme/app#x", "../../etc/passwd"} {
			wantAPIError(t, e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape(id)), http.StatusBadRequest, core.CodeInvalidArgument)
		}
	})
}

func TestIssueWithoutAProjectionStillAppears(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 4, toReady...)
		var detail control.IssueDetail
		getJSON(t, e.api(http.MethodGet, "/api/v1/issues/"+url.PathEscape("acme/app#4")), &detail)
		if detail.ID != "acme/app#4" || detail.Title != "" {
			t.Errorf("detail = %+v, want the record with no title", detail)
		}
	})
}

func TestAPIFailureHidesTheCause(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		if err := e.store.Close(); err != nil {
			t.Fatal(err)
		}
		rec := e.api(http.MethodGet, "/api/v1/factory/status")
		if rec.Code < 500 {
			t.Fatalf("status = %d, want a server error", rec.Code)
		}
		var body struct {
			Error *core.Error `json:"error"`
		}
		getJSON(t, rec, &body)
		if body.Error == nil || !body.Error.Code.Valid() {
			t.Fatalf("body = %s, want a typed error", rec.Body.String())
		}
		for _, leak := range []string{"sql:", "sqlite", "pgx", e.db.DSN} {
			if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(leak)) {
				t.Errorf("the error leaks %q: %s", leak, rec.Body.String())
			}
		}
	})
}

func TestAPIOnlyReads(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			rec := e.api(method, "/api/v1/issues")
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("%s /api/v1/issues = %d, want a client error", method, rec.Code)
			}
		}
	})
}
