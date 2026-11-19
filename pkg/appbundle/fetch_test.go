package appbundle

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTLSFetcher(t *testing.T, h http.HandlerFunc, maxBytes int64) (*HTTPFetcher, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	f := NewHTTPFetcher(5*time.Second, maxBytes)
	f.Client.Transport = srv.Client().Transport
	return f, srv.URL
}

func TestHTTPFetcher(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "archive") })
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/announced", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	mux.HandleFunc("/streamed", func(w http.ResponseWriter, _ *http.Request) {
		for range 10 {
			_, _ = io.WriteString(w, strings.Repeat("x", 10))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/downgrade", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/ok", http.StatusFound)
	})
	f, base := newTLSFetcher(t, mux.ServeHTTP, 20)

	for name, tc := range map[string]struct{ url, body, want string }{
		"ok":                {base + "/ok", "archive", ""},
		"not found":         {base + "/missing", "", "404"},
		"announced too big": {base + "/announced", "", "announced"},
		"streamed too big":  {base + "/streamed", "", "larger than allowed"},
		"plain http":        {strings.Replace(base, "https://", "http://", 1) + "/ok", "", "only https"},
		"file scheme":       {"file:///etc/passwd", "", "only https"},
		"redirect to http":  {base + "/downgrade", "", "not https"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body, err := f.Fetch(context.Background(), tc.url)
			var got []byte
			if err == nil {
				got, err = io.ReadAll(body)
				_ = body.Close()
			}
			if tc.want == "" {
				if err != nil || string(got) != tc.body {
					t.Fatalf("got %q, %v; want %q", got, err, tc.body)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
