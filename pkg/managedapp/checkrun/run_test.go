package checkrun

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// steppingClock advances by step on every reading, so latency is exact.
func steppingClock(step time.Duration) func() time.Time {
	t := time.Unix(0, 0)
	return func() time.Time { t = t.Add(step); return t }
}

// shop answers like a small store: a nonce, a product list and a checkout
// that succeeds only with the captured nonce and product.
func shop(t *testing.T, checkout string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cart", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Nonce", "n-1") })
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `[{"id":42}]`) })
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID int }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("Nonce") != "n-1" || in.ID != 42 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"payment_result":{"payment_status":"`+checkout+`"},"secret_note":"visitor text"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func purchase() managedapp.Check {
	return managedapp.Check{Name: "purchase", MaxLatencyMS: 100, Steps: []managedapp.Step{
		{Path: "/cart", CaptureHeader: map[string]string{"nonce": "Nonce"}},
		{Path: "/products", CaptureJSON: map[string]string{"product": "0.id"}},
		{Method: "POST", Path: "/checkout", Headers: map[string]string{"Nonce": "${nonce}"}, Body: `{"id":${product}}`,
			JSON: map[string]string{"payment_result.payment_status": "success"}},
	}}
}

func TestAJourneyCarriesCapturedValuesAndPasses(t *testing.T) {
	t.Parallel()
	srv := shop(t, "success")
	r := Runner{BaseURL: srv.URL, Client: srv.Client(), Now: steppingClock(time.Millisecond)}
	if res := r.Run(context.Background(), purchase()); !res.Passed || res.Step != -1 {
		t.Fatalf("%+v", res)
	}
}

// A checkout that answers 200 but does not take the payment fails, and the
// result names only the Spec's field, never the response's text.
func TestACheckoutThatAnswers200WithoutAnOrderFails(t *testing.T) {
	t.Parallel()
	srv := shop(t, "failure")
	r := Runner{BaseURL: srv.URL, Client: srv.Client(), Now: steppingClock(time.Millisecond)}
	res := r.Run(context.Background(), purchase())
	if res.Passed || res.Reason != ReasonValue || res.Step != 2 || res.Field != "payment_result.payment_status" {
		t.Fatalf("%+v", res)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "visitor text") || strings.Contains(string(raw), "failure") {
		t.Fatalf("the result carries response content: %s", raw)
	}
}

func TestEveryKindOfFailureIsReportedAsAFact(t *testing.T) {
	t.Parallel()
	srv := shop(t, "success")
	cases := map[string]struct {
		check managedapp.Check
		clock time.Duration
		want  Reason
	}{
		"slow":            {purchase(), 150 * time.Millisecond, ReasonSlow},
		"wrong status":    {managedapp.Check{Name: "x", Steps: []managedapp.Step{{Path: "/missing"}}}, time.Millisecond, ReasonStatus},
		"missing text":    {managedapp.Check{Name: "x", Steps: []managedapp.Step{{Path: "/products", Contains: "prices"}}}, time.Millisecond, ReasonContent},
		"nothing to keep": {managedapp.Check{Name: "x", Steps: []managedapp.Step{{Path: "/products", CaptureJSON: map[string]string{"p": "3.id"}}}}, time.Millisecond, ReasonCapture},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := Runner{BaseURL: srv.URL, Client: srv.Client(), Now: steppingClock(c.clock)}
			if res := r.Run(context.Background(), c.check); res.Passed || res.Reason != c.want {
				t.Fatalf("want %s, got %+v", c.want, res)
			}
		})
	}
	unreachable := Runner{BaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}}
	if res := unreachable.Run(context.Background(), purchase()); res.Passed || res.Reason != ReasonUnreachable {
		t.Fatalf("%+v", res)
	}
}
