package report_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

type failingStore struct{}

func (failingStore) Save(context.Context, report.Report) (report.Report, bool, error) {
	return report.Report{}, false, errors.New("disk full")
}

func newHandler(t *testing.T, mutate func(*report.Config)) (http.Handler, *report.MemoryStore, *clock) {
	t.Helper()
	store := report.NewMemoryStore(100)
	c := &clock{now: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}
	cfg := report.Config{Store: store, Redactor: redact.New(redact.Config{}), Clock: c, PerClientLimit: 3, GlobalLimit: 100, Window: time.Minute}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := report.NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, store, c
}

func post(h http.Handler, from, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(body))
	req.RemoteAddr = from + ":5555"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const jsonType = "application/json"

func TestAcceptedReportIsStoredAsRedactedDataAndNotEchoed(t *testing.T) {
	t.Parallel()
	h, store, _ := newHandler(t, nil)
	rec := post(h, "198.51.100.1", jsonType+"; charset=utf-8",
		`{"title":"Checkout fails","body":"<script>x</script> my mail is jane.doe@example.org"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "script") || strings.Contains(rec.Body.String(), "Checkout") {
		t.Errorf("the response must not echo the content: %s", rec.Body)
	}
	got := store.All()
	if len(got) != 1 || got[0].Title != "Checkout fails" || strings.Contains(got[0].Body, "jane.doe@example.org") ||
		!strings.Contains(got[0].Body, "<script>x</script>") || got[0].ID == "" {
		t.Fatalf("stored = %+v", got)
	}
}

func TestDuplicateReportsAreCountedNotStoredTwice(t *testing.T) {
	t.Parallel()
	h, store, _ := newHandler(t, nil)
	first := post(h, "198.51.100.1", jsonType, `{"title":"Broken","body":"The page   is blank"}`)
	again := post(h, "198.51.100.2", jsonType, `{"title":"broken","body":"the page is\nblank"}`)
	if first.Code != http.StatusAccepted || again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"duplicate"`) {
		t.Fatalf("first %d %s, again %d %s", first.Code, first.Body, again.Code, again.Body)
	}
	got := store.All()
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("one report seen twice, got %+v", got)
	}
}

func TestReportLimitsAreEnforced(t *testing.T) {
	t.Parallel()
	h, store, _ := newHandler(t, func(c *report.Config) { c.MaxBytes, c.PerClientLimit = 512, 100 })
	cases := map[string]struct {
		method, contentType, body string
		want                      int
	}{
		"wrong method":      {http.MethodGet, jsonType, "", http.StatusMethodNotAllowed},
		"not json":          {http.MethodPost, "text/plain", `{"title":"a","body":"b"}`, http.StatusUnsupportedMediaType},
		"too large":         {http.MethodPost, jsonType, `{"title":"a","body":"` + strings.Repeat("x", 600) + `"}`, http.StatusRequestEntityTooLarge},
		"unknown field":     {http.MethodPost, jsonType, `{"title":"a","body":"b","role":"owner"}`, http.StatusBadRequest},
		"two documents":     {http.MethodPost, jsonType, `{"title":"a","body":"b"}{"title":"c","body":"d"}`, http.StatusBadRequest},
		"blank body":        {http.MethodPost, jsonType, `{"title":"a","body":"   "}`, http.StatusBadRequest},
		"missing title":     {http.MethodPost, jsonType, `{"body":"b"}`, http.StatusBadRequest},
		"long title":        {http.MethodPost, jsonType, `{"title":"` + strings.Repeat("t", 201) + `","body":"b"}`, http.StatusBadRequest},
		"control character": {http.MethodPost, jsonType, `{"title":"a\u0000b","body":"b"}`, http.StatusBadRequest},
		"malformed":         {http.MethodPost, jsonType, `{"title":`, http.StatusBadRequest},
	}
	for name, c := range cases {
		req := httptest.NewRequest(c.method, report.Path, strings.NewReader(c.body))
		req.RemoteAddr = "198.51.100.9:1"
		req.Header.Set("Content-Type", c.contentType)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, c.want, rec.Body)
		}
	}
	if n := len(store.All()); n != 0 {
		t.Fatalf("refused reports must not be stored, got %d", n)
	}
}

func TestRateLimitPerClientAndOverall(t *testing.T) {
	t.Parallel()
	h, _, c := newHandler(t, func(c *report.Config) { c.GlobalLimit = 5 })
	body := func(i int) string { return `{"title":"t","body":"report ` + strings.Repeat("x", i+1) + `"}` }
	for i := range 3 {
		if rec := post(h, "198.51.100.1", jsonType, body(i)); rec.Code != http.StatusAccepted {
			t.Fatalf("report %d: %d", i, rec.Code)
		}
	}
	limited := post(h, "198.51.100.1", jsonType, body(9))
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("a fourth report in the window is refused: %d %v", limited.Code, limited.Header())
	}
	// Other clients share the overall limit of five.
	if rec := post(h, "198.51.100.2", jsonType, body(10)); rec.Code != http.StatusAccepted {
		t.Fatalf("another client: %d", rec.Code)
	}
	if rec := post(h, "198.51.100.3", jsonType, body(11)); rec.Code != http.StatusAccepted {
		t.Fatalf("another client: %d", rec.Code)
	}
	if rec := post(h, "198.51.100.4", jsonType, body(12)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the overall limit must hold across clients: %d", rec.Code)
	}
	c.After(time.Minute)
	if rec := post(h, "198.51.100.1", jsonType, body(13)); rec.Code != http.StatusAccepted {
		t.Fatalf("a new window allows reports again: %d", rec.Code)
	}
}

func TestClientTableIsBoundedAndFailsClosed(t *testing.T) {
	t.Parallel()
	h, _, _ := newHandler(t, func(c *report.Config) { c.MaxClients = 2 })
	post(h, "198.51.100.1", jsonType, `{"title":"t","body":"a"}`)
	post(h, "198.51.100.2", jsonType, `{"title":"t","body":"b"}`)
	if rec := post(h, "198.51.100.3", jsonType, `{"title":"t","body":"c"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a full client table refuses new clients in the window: %d", rec.Code)
	}
}

func TestStoreFailureIsReportedNotHidden(t *testing.T) {
	t.Parallel()
	h, _, _ := newHandler(t, func(c *report.Config) { c.Store = failingStore{} })
	rec := post(h, "198.51.100.1", jsonType, `{"title":"t","body":"b"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	if b, _ := io.ReadAll(rec.Body); strings.Contains(string(b), "disk full") {
		t.Errorf("internal causes must not reach an anonymous caller: %s", b)
	}
}

func TestHandlerNeedsAStoreAndARedactor(t *testing.T) {
	t.Parallel()
	if _, err := report.NewHandler(report.Config{Redactor: redact.New(redact.Config{})}); err == nil {
		t.Error("no store must be refused")
	}
	if _, err := report.NewHandler(report.Config{Store: report.NewMemoryStore(1)}); err == nil {
		t.Error("no redactor must be refused")
	}
	if _, err := report.NewHandler(report.Config{Store: report.NewMemoryStore(1), Redactor: redact.New(redact.Config{}), MaxBytes: -1}); err == nil {
		t.Error("negative limits must be refused")
	}
}

func TestStoreKeepsReportsForTheirRetentionPeriodOnly(t *testing.T) {
	t.Parallel()
	h, store, c := newHandler(t, nil)
	post(h, "198.51.100.1", jsonType, `{"title":"old","body":"a"}`)
	c.After(48 * time.Hour)
	post(h, "198.51.100.1", jsonType, `{"title":"new","body":"b"}`)
	n, err := store.Purge(context.Background(), c.Now().Add(-24*time.Hour))
	if err != nil || n != 1 || len(store.All()) != 1 || store.All()[0].Title != "new" {
		t.Fatalf("purge removed %d (%v), left %+v", n, err, store.All())
	}
}

// The defaults bound acceptance per hour: five reports from one client and
// sixty from everyone together, counted again an hour after a window began.
func TestDefaultLimitsArePerHour(t *testing.T) {
	t.Parallel()
	if report.DefaultPerClientLimit != 5 || report.DefaultGlobalLimit != 60 || report.DefaultWindow != time.Hour {
		t.Fatalf("defaults %d per client, %d overall per %s; want 5 and 60 per hour",
			report.DefaultPerClientLimit, report.DefaultGlobalLimit, report.DefaultWindow)
	}
	h, _, c := newHandler(t, func(c *report.Config) { c.PerClientLimit, c.GlobalLimit, c.Window = 0, 0, 0 })
	body := func(i int) string { return fmt.Sprintf(`{"title":"t","body":"report %d"}`, i) }
	for i := range 5 {
		if rec := post(h, "198.51.100.1", jsonType, body(i)); rec.Code != http.StatusAccepted {
			t.Fatalf("report %d: %d", i, rec.Code)
		}
	}
	c.After(59 * time.Minute)
	if rec := post(h, "198.51.100.1", jsonType, body(5)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a sixth report from one client within the hour: %d, want refused", rec.Code)
	}
	accepted := 5
	for n := 2; n < 40; n++ {
		for i := range 5 {
			if rec := post(h, fmt.Sprintf("198.51.100.%d", n), jsonType, body(n*10+i)); rec.Code == http.StatusAccepted {
				accepted++
			}
		}
	}
	if accepted != 60 {
		t.Fatalf("%d reports accepted within the hour from all clients, want 60", accepted)
	}
	c.After(time.Minute)
	if rec := post(h, "198.51.100.1", jsonType, body(999)); rec.Code != http.StatusAccepted {
		t.Fatalf("a report an hour after the window began: %d", rec.Code)
	}
}

// Invisible characters are dropped before the text is redacted and
// fingerprinted: a secret split by a zero-width space, a soft hyphen or a
// byte order mark is stored redacted, the same report with or without
// them is one report, and a text of nothing but them is blank.
func TestInvisibleCharactersAreDroppedBeforeRedaction(t *testing.T) {
	t.Parallel()
	h, store, _ := newHandler(t, nil)
	secret := "gh" + "p_" + strings.Repeat("A1b2", 9)
	split := strings.Join(strings.Split(secret, ""), "\u200b\u00ad\ufeff")
	first := post(h, "198.51.100.1", jsonType, `{"title":"Login\u200b fails","body":"token `+split+`"}`)
	again := post(h, "198.51.100.2", jsonType, `{"title":"Login fails","body":"token `+secret+`"}`)
	if first.Code != http.StatusAccepted || again.Code != http.StatusOK {
		t.Fatalf("first %d %s, again %d %s; want one report counted twice", first.Code, first.Body, again.Code, again.Body)
	}
	got := store.All()
	if len(got) != 1 || strings.Contains(got[0].Body, secret) || strings.ContainsAny(got[0].Title+got[0].Body, "\u200b\u00ad\ufeff") {
		t.Fatalf("stored = %+v", got)
	}
	if rec := post(h, "198.51.100.3", jsonType, `{"title":"\u200b\ufeff","body":"b"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a title of invisible characters only: %d, want refused", rec.Code)
	}
}
