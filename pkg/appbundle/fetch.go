package appbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Fetcher downloads an archive. Assemble verifies and bounds what it
// returns, so an implementation need not be trusted with integrity.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// HTTPFetcher downloads over https only, with a timeout per download and a
// bound on the bytes it returns.
type HTTPFetcher struct {
	Client   *http.Client
	MaxBytes int64
}

// NewHTTPFetcher returns an HTTPFetcher whose downloads time out after
// timeout and fail past maxBytes. Redirects must stay on https.
func NewHTTPFetcher(timeout time.Duration, maxBytes int64) *HTTPFetcher {
	return &HTTPFetcher{
		MaxBytes: maxBytes,
		Client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("redirect to %s is not https", req.URL.Redacted())
				}
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// Fetch implements Fetcher.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("fetch %q: only https URLs are fetched", rawURL)
	}
	if f.MaxBytes <= 0 {
		return nil, errors.New("fetch: MaxBytes must be positive")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", u.Redacted(), err)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", u.Redacted(), err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("fetch %s: status %s", u.Redacted(), resp.Status)
	}
	if resp.ContentLength > f.MaxBytes {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("fetch %s: %w: %d bytes announced, limit %d", u.Redacted(), errLimit, resp.ContentLength, f.MaxBytes)
	}
	return &cappedBody{r: resp.Body, left: f.MaxBytes}, nil
}

// cappedBody fails, rather than truncates, past its byte budget.
type cappedBody struct {
	r    io.ReadCloser
	left int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return 0, fmt.Errorf("%w: download is larger than allowed", errLimit)
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.r.Close() }

// openVerified returns an open cached archive whose bytes hash to
// e.SHA256, downloading it when the cache has no good copy. The hash is
// taken on the same open file the caller unzips, so what is checked is what
// is read. The cache is content-addressed: <sha256>.zip.
func openVerified(ctx context.Context, f Fetcher, cacheDir string, e Entry, maxBytes int64) (*os.File, error) {
	name := filepath.Join(cacheDir, e.SHA256+".zip")
	if file, err := openIfHashMatches(name, e.SHA256); err == nil {
		return file, nil
	}
	body, err := f.Fetch(ctx, e.URL())
	if err != nil {
		return nil, fmt.Errorf("%s %s %s: %w", e.Kind, e.Slug, e.Version, err)
	}
	tmp, err := os.CreateTemp(cacheDir, ".fetch-*")
	if err != nil {
		_ = body.Close()
		return nil, fmt.Errorf("cache %s: %w", e.Slug, err)
	}
	// Hash on the way in so a download that does not match is never cached.
	h := sha256.New()
	_, copyErr := copyCapped(io.MultiWriter(tmp, h), body, maxBytes, e.URL())
	err = errors.Join(copyErr, body.Close(), tmp.Close())
	if got := hex.EncodeToString(h.Sum(nil)); err == nil && got != e.SHA256 {
		err = fmt.Errorf("archive has sha256 %s, the lockfile pins %s", got, e.SHA256)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("%s %s %s: %w", e.Kind, e.Slug, e.Version, err)
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("cache %s: %w", e.Slug, err)
	}
	file, err := openIfHashMatches(name, e.SHA256)
	if err != nil {
		_ = os.Remove(name)
		return nil, fmt.Errorf("%s %s %s: %w", e.Kind, e.Slug, e.Version, err)
	}
	return file, nil
}

// openIfHashMatches opens name and returns it, rewound, only if its
// content hashes to want.
func openIfHashMatches(name, want string) (*os.File, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = file.Close()
		return nil, fmt.Errorf("archive has sha256 %s, the lockfile pins %s", got, want)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// copyCapped copies src to dst and fails once more than limit bytes come.
func copyCapped(dst io.Writer, src io.Reader, limit int64, what string) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("%w: %s is larger than %d bytes", errLimit, what, limit)
	}
	return n, nil
}
