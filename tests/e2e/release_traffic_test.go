//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// traffic is real load on the application through the edge, enough for the
// release monitor's sample floor in each window.
type traffic struct {
	sent, failed atomic.Int64
	stop         context.CancelFunc
	done         sync.WaitGroup
}

// startTraffic sends about sixty requests a second to the edge until the
// returned traffic is stopped or the test ends.
func startTraffic(t *testing.T, d releaseDeployment) *traffic {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tr := &traffic{stop: cancel}
	client := &http.Client{Timeout: 5 * time.Second}
	tr.done.Add(1)
	go func() {
		defer tr.done.Done()
		tick := time.NewTicker(16 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.edgeURL()+"/", nil)
			resp, err := client.Do(req)
			if ctx.Err() != nil {
				return
			}
			tr.sent.Add(1)
			if err != nil {
				tr.failed.Add(1)
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 500 {
				tr.failed.Add(1)
			}
		}
	}()
	t.Cleanup(tr.halt)
	return tr
}

// halt stops the load and waits for its last request.
func (tr *traffic) halt() {
	tr.stop()
	tr.done.Wait()
}
