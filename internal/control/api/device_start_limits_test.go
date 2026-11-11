package api_test

import (
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/control/api"
	"github.com/bestagentkits/sisf/internal/intake/report"
)

// One address may start a few login requests a minute; the next is
// refused with 429 and the time to retry, and another address is not held
// up by it.
func TestALoginRequestOverTheSourcesRateIsRefusedWithATimeToRetry(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry, keyedBySourceHeader)
	b := e.browser(t)
	start := func(source string) reply {
		return b.do(t, http.MethodPost, api.DeviceStartPath, `{"name":"cli"}`,
			map[string]string{"Content-Type": "application/json", sourceHeader: source})
	}
	for n := range report.StartsPerSource {
		if r := start("203.0.113.9"); r.status != http.StatusOK {
			t.Fatalf("start %d: %d %s", n, r.status, r.body)
		}
	}
	if r := start("203.0.113.9"); r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("a start over the source's rate: %d, Retry-After %q; want 429 with a time to retry", r.status, r.header.Get("Retry-After"))
	}
	if r := start("203.0.113.10"); r.status != http.StatusOK {
		t.Fatalf("another address: %d %s", r.status, r.body)
	}
}
