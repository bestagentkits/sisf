package fakegithub

import (
	"net/http"
	"strconv"
	"time"
)

// apiFault is a canned answer served instead of handling the next request.
type apiFault struct {
	status  int
	header  map[string]string
	message string
}

func (f apiFault) write(w http.ResponseWriter) {
	for k, v := range f.header {
		w.Header().Set(k, v)
	}
	writeError(w, f.status, f.message)
}

func (s *Server) queueFaults(n int, f apiFault) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	for range n {
		s.faults = append(s.faults, f)
	}
}

func (s *Server) nextFault() (apiFault, bool) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	if len(s.faults) == 0 {
		return apiFault{}, false
	}
	f := s.faults[0]
	s.faults = s.faults[1:]
	return f, true
}

// LimitNext answers the next n API calls with GitHub's secondary rate limit:
// 429 with a Retry-After of the given number of seconds.
func (s *Server) LimitNext(n int, retryAfter time.Duration) {
	s.queueFaults(n, apiFault{
		status: http.StatusTooManyRequests, message: "You have exceeded a secondary rate limit.",
		header: map[string]string{"Retry-After": strconv.Itoa(int(retryAfter.Seconds()))},
	})
}

// LimitPrimaryNext answers the next n API calls with the primary rate limit:
// 403, no requests remaining, and a reset instant.
func (s *Server) LimitPrimaryNext(n int, reset time.Time) {
	s.queueFaults(n, apiFault{
		status: http.StatusForbidden, message: "API rate limit exceeded",
		header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)},
	})
}

// FailNext answers the next n API calls with the given status, for example
// 502 or 503, to simulate an outage.
func (s *Server) FailNext(n, status int) {
	s.queueFaults(n, apiFault{status: status, message: "Server Error"})
}
