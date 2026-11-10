package install

import (
	"context"
	"io"
	"net/http"
	"time"
)

// maxProbeBytes bounds how much of an answer the doctor reads.
const maxProbeBytes = 1 << 20

// probeTimeout bounds one probe when the installer has no client of its own.
const probeTimeout = 5 * time.Second

// httpGet fetches a URL with the installer's client. Redirects are never
// followed, so a token cannot be handed on to another host. The headers are
// sent as given; the error carries no header, so a bearer token is never
// part of it.
func (in *Installer) httpGet(ctx context.Context, url string, header http.Header) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, invalidf("the probe address is not valid")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := http.Client{Timeout: probeTimeout}
	if in.HTTP != nil {
		client = *in.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, unavailablef(err, "no answer from %s", req.URL.Host)
	}
	defer func() { _ = resp.Body.Close() }() // the answer was read or abandoned already
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBytes))
	if err != nil {
		return resp.StatusCode, nil, unavailablef(err, "the answer from %s was cut off", req.URL.Host)
	}
	return resp.StatusCode, body, nil
}
