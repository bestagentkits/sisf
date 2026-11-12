package github

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// nextLink returns the rel="next" target of a Link header.
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		target, rel, ok := strings.Cut(part, ";")
		if ok && strings.Contains(rel, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(target), "<>")
		}
	}
	return ""
}

// getPages reads a listing page by page, calling each with the body of every
// page, and returns the first response so the caller can read its ETag. A
// conditional first request that gets 304 stops there.
//
// The next-page link comes from the response, so it is data: it is followed
// only on the API host, and only up to MaxPages.
func (c *RESTClient) getPages(ctx context.Context, path string, query url.Values, header http.Header, each func(apiResponse) error) (apiResponse, error) {
	return c.getPagesUpTo(ctx, c.cfg.MaxPages, path, query, header, each)
}

// getPagesUpTo is getPages that follows at most limit pages, never more
// than MaxPages; a listing with more fails with LIMIT_EXCEEDED.
func (c *RESTClient) getPagesUpTo(ctx context.Context, limit int, path string, query url.Values, header http.Header, each func(apiResponse) error) (apiResponse, error) {
	first, more, err := c.getPagesBounded(ctx, limit, path, query, header, each)
	if err == nil && more {
		err = core.NewError(core.CodeLimitExceeded, "the listing has more pages than this client follows")
	}
	return first, err
}

// getPagesBounded is getPagesUpTo that stops at the bound without failing:
// more reports that the listing goes on past the last page read.
func (c *RESTClient) getPagesBounded(ctx context.Context, limit int, path string, query url.Values, header http.Header, each func(apiResponse) error) (first apiResponse, more bool, err error) {
	if limit <= 0 || limit > c.cfg.MaxPages {
		limit = c.cfg.MaxPages
	}
	first, err = c.call(ctx, http.MethodGet, path, query, nil, header)
	if err != nil || first.status == http.StatusNotModified {
		return first, false, err
	}
	resp := first
	for page := 1; ; page++ {
		if err := each(resp); err != nil {
			return first, false, err
		}
		next := nextLink(resp.header)
		if next == "" {
			return first, false, nil
		}
		if page >= limit {
			return first, true, nil
		}
		if resp, err = c.callURL(ctx, http.MethodGet, next, nil, nil); err != nil {
			return first, false, err
		}
	}
}
