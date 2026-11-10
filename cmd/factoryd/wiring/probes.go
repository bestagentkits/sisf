package wiring

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

// HTTPProbe checks a zone by asking its health endpoint; only a 200 counts.
func HTTPProbe(url string) control.HealthProbe {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return control.HealthProbeFunc(func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return core.NewError(core.CodeInvalidArgument, "the health probe address is not a URL")
		}
		resp, err := client.Do(req)
		if err != nil {
			return core.NewError(core.CodeUnavailable, "the zone does not answer")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return core.NewError(core.CodeUnavailable, "the zone answers "+http.StatusText(resp.StatusCode))
		}
		return nil
	})
}

// ZoneProbes probes the application through its health URL and the agent
// zone through the dispatcher's.
func ZoneProbes(appURL, dispatcherURL string) map[core.Zone]control.HealthProbe {
	return map[core.Zone]control.HealthProbe{
		core.ZoneApplication: HTTPProbe(orDefault(appURL, DefaultAppHealthURL)),
		core.ZoneAgent:       HTTPProbe(orDefault(dispatcherURL, DefaultDispatcherHealthURL)),
	}
}
