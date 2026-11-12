package fakegithub_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func httptestRequest(t *testing.T, h http.Handler, path, auth string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func openThree(t *testing.T, e *env) {
	t.Helper()
	for _, title := range []string{"a", "b", "c"} {
		if _, err := e.fake.OpenIssue(testRepo, e.fake.Person("alice", "OWNER"), title, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDroppedDeliveriesAreRecordedButNeverSent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.DropNext(1)
	openThree(t, e)
	if got := len(e.hook.deliveries()); got != 2 {
		t.Fatalf("receiver saw %d deliveries, want 2", got)
	}
	all := e.fake.Deliveries()
	if len(all) != 3 || !all[0].Dropped || all[0].Attempts != 0 || all[1].Dropped {
		t.Fatalf("recorded deliveries = %+v", all)
	}
}

func TestDuplicatedDeliveriesAreSentTwiceWithTheSameID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.DuplicateNext(1)
	openThree(t, e)
	got := e.hook.deliveries()
	if len(got) != 4 || got[0].ID != got[1].ID || got[1].ID == got[2].ID {
		t.Fatalf("deliveries = %+v", got)
	}
}

func TestHeldDeliveriesCanBeReleasedInReverseOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.Hold()
	openThree(t, e)
	if n := len(e.hook.deliveries()); n != 0 {
		t.Fatalf("%d deliveries leaked while held", n)
	}
	e.fake.Release(true)
	got := e.hook.deliveries()
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	if !slices.IsSortedFunc(ids, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Fatalf("released order = %v, want newest first", ids)
	}
	e.fake.Release(false)
	if n := len(e.hook.deliveries()); n != 3 {
		t.Fatalf("a second release sent %d extra deliveries", n-3)
	}
}

func TestRedeliveryReusesTheOriginalIDForADroppedDelivery(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.DropNext(1)
	openThree(t, e)
	dropped := e.fake.Deliveries()[0]
	if err := e.fake.Redeliver(dropped.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.hook.deliveries(); got[len(got)-1].ID != dropped.ID {
		t.Fatalf("redelivery used id %q, want %q", got[len(got)-1].ID, dropped.ID)
	}
	if after := e.fake.Deliveries()[0]; after.Dropped || after.Attempts != 1 || after.Status != http.StatusAccepted {
		t.Fatalf("delivery after redelivery = %+v", after)
	}
	if err := e.fake.Redeliver("no-such-delivery"); err == nil {
		t.Fatal("redelivering an unknown id must fail")
	}
}

func TestRedeliveryThroughTheAppHookAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.DropNext(1)
	openThree(t, e)
	status, body, _ := e.call(t, "GET", "/app/hook/deliveries", nil)
	if status != 200 || len(body) == 0 {
		t.Fatalf("list deliveries: %d", status)
	}
	if s, body, _ := e.call(t, "POST", "/app/hook/deliveries/1/attempts", nil); s != http.StatusAccepted {
		t.Fatalf("redeliver: %d %s", s, body)
	}
	if s, _, _ := e.call(t, "POST", "/app/hook/deliveries/99/attempts", nil); s != http.StatusNotFound {
		t.Fatalf("redeliver unknown: %d, want 404", s)
	}
	if n := len(e.hook.deliveries()); n != 3 {
		t.Fatalf("receiver saw %d deliveries, want 3", n)
	}
}

func TestAReceiverThatFailsIsRecordedAndCanBeRetried(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.hook.mu.Lock()
	e.hook.code = http.StatusServiceUnavailable
	e.hook.mu.Unlock()
	openThree(t, e)
	if d := e.fake.Deliveries()[0]; d.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", d.Status)
	}
}

func TestInjectedRateLimitsAndOutagesAnswerTheNextCallsOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fake.LimitNext(1, 7*time.Second)
	s, _, h := e.call(t, "GET", "/repos/acme/app/issues", nil)
	if s != http.StatusTooManyRequests || h.Get("Retry-After") != "7" {
		t.Fatalf("secondary limit: %d retry-after %q", s, h.Get("Retry-After"))
	}
	reset := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	e.fake.LimitPrimaryNext(1, reset)
	s, _, h = e.call(t, "GET", "/repos/acme/app/issues", nil)
	if s != http.StatusForbidden || h.Get("X-RateLimit-Remaining") != "0" || h.Get("X-RateLimit-Reset") != itoa(reset.Unix()) {
		t.Fatalf("primary limit: %d %v", s, h)
	}
	e.fake.FailNext(1, http.StatusBadGateway)
	if s, _, _ := e.call(t, "GET", "/repos/acme/app/issues", nil); s != http.StatusBadGateway {
		t.Fatalf("outage: %d", s)
	}
	if s, _, _ := e.call(t, "GET", "/repos/acme/app/issues", nil); s != http.StatusOK {
		t.Fatalf("after the faults: %d, want 200", s)
	}
	if reqs := e.fake.Requests(); len(reqs) != 4 || reqs[0].Status != 429 {
		t.Fatalf("request log = %+v", reqs)
	}
}

func TestControlEndpointsConfigureTheFakeOverHTTP(t *testing.T) {
	t.Parallel()
	fake := fakegithub.New()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	post := func(path, body string) int {
		resp, err := testClient.Post(srv.URL+path, "application/json", stringsReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if s := post("/_sim/repos", `{"repo":"acme/app"}`); s != http.StatusNoContent {
		t.Fatalf("add repo: %d", s)
	}
	if s := post("/_sim/repos", `{"repo":"nonsense"}`); s != http.StatusBadRequest {
		t.Fatalf("bad repo: %d, want 400", s)
	}
	if s := post("/_sim/faults", `{"drop":1,"duplicate":2,"hold":true,"release":"reversed"}`); s != http.StatusNoContent {
		t.Fatalf("faults: %d", s)
	}
	if s := post("/_sim/faults", `{"release":"sideways"}`); s != http.StatusBadRequest {
		t.Fatalf("bad release: %d, want 400", s)
	}
	if s := post("/_sim/webhook", `{"url":"","secret":"x"}`); s != http.StatusNoContent {
		t.Fatalf("webhook: %d", s)
	}
}
