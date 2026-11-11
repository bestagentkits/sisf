package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// apiResponse is a completed exchange with the API.
type apiResponse struct {
	status int
	header http.Header
	body   []byte
}

// remoteMessage extracts GitHub's "message" for operators. It is remote
// data: shortened and stripped of line breaks before it is kept.
func remoteMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	msg := strings.Join(strings.Fields(m.Message), " ")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// statusError maps an unsuccessful status to a typed error. The token never
// appears in it.
func statusError(method, path string, resp apiResponse) error {
	what := fmt.Sprintf("GitHub answered %d to %s %s", resp.status, method, path)
	var err *core.Error
	switch {
	case resp.status == http.StatusUnauthorized:
		err = core.NewError(core.CodeUnauthenticated, what+": the installation token was refused")
	case resp.status == http.StatusForbidden:
		err = core.NewError(core.CodePermissionDenied, what+": the installation lacks this permission")
	case resp.status == http.StatusNotFound || resp.status == http.StatusGone:
		err = core.NewError(core.CodeNotFound, what)
	case resp.status == http.StatusConflict:
		err = core.NewError(core.CodeConflict, what)
	case resp.status == http.StatusUnprocessableEntity && strings.Contains(string(resp.body), "already_exists"):
		err = core.NewError(core.CodeAlreadyExists, what)
	case resp.status >= 400 && resp.status < 500:
		err = core.NewError(core.CodeInvalidArgument, what)
	default:
		err = core.NewError(core.CodeInternal, what+": unexpected status")
	}
	err.Details = map[string]string{"status": fmt.Sprint(resp.status)}
	if msg := remoteMessage(resp.body); msg != "" {
		err.Details["github_message"] = msg
	}
	return err
}

// IsRateLimit reports whether GitHub's answer, its status, headers and body,
// is a primary or secondary rate limit. A 429 always is; a 403 is a limit
// only when it says so: Retry-After, no remaining requests, or a message
// naming the limit. Any other 403 is a real permission failure.
func IsRateLimit(status int, header http.Header, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status == http.StatusForbidden &&
		(header.Get("Retry-After") != "" || header.Get("X-RateLimit-Remaining") == "0" ||
			strings.Contains(strings.ToLower(remoteMessage(body)), "rate limit"))
}

// rateLimited reports whether the response is a rate limit (IsRateLimit)
// and how long GitHub asks to wait. Any other 403 is never retried.
func (c *RESTClient) rateLimited(resp apiResponse) (bool, time.Duration) {
	if !IsRateLimit(resp.status, resp.header, resp.body) {
		return false, 0
	}
	retryAfter := resp.header.Get("Retry-After")
	remaining := resp.header.Get("X-RateLimit-Remaining")
	if secs, err := parseSeconds(retryAfter); err == nil {
		return true, secs
	}
	if remaining == "0" {
		if reset, err := parseSeconds(resp.header.Get("X-RateLimit-Reset")); err == nil {
			if wait := time.Unix(int64(reset/time.Second), 0).Sub(c.clock.Now()); wait > 0 {
				return true, wait
			}
			return true, time.Second
		}
	}
	// GitHub asks secondary limits without a hint to wait at least a minute.
	return true, time.Minute
}

func parseSeconds(v string) (time.Duration, error) {
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
		return 0, fmt.Errorf("not a number of seconds")
	}
	return time.Duration(n) * time.Second, nil
}

func rateLimitError(method, path string, wait time.Duration, exhausted bool) error {
	why := "the wait is longer than this client will block for"
	if exhausted {
		why = "the limit persisted through every retry"
	}
	err := core.NewError(core.CodeRateLimited, fmt.Sprintf("GitHub rate limit on %s %s: %s", method, path, why))
	err.Details = map[string]string{"retry_after_seconds": fmt.Sprint(int(wait.Seconds()))}
	return err
}

func itoa(n int) string { return fmt.Sprint(n) }
