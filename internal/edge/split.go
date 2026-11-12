package edge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"regexp"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RevisionHeader names the response header that says which revision served
// the request. The edge sets it; a value the upstream sent is replaced.
const RevisionHeader = "X-Sisf-Revision"

var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)

// Target is one revision and where it runs.
type Target struct {
	// Revision labels the responses and the counters, usually the release
	// digest.
	Revision string `json:"revision"`
	// URL is the base URL of the upstream, as ParseUpstream reads it.
	URL string `json:"url"`
}

// Split says how requests are divided between two revisions.
type Split struct {
	Stable Target  `json:"stable"`
	Canary *Target `json:"canary,omitempty"`
	// CanaryPercent is the share of requests, 1 to 99, sent to Canary.
	CanaryPercent int `json:"canary_percent"`
}

// validate checks the revisions, the upstreams and the percentage.
func (s Split) validate() error {
	targets := []Target{s.Stable}
	if s.Canary != nil {
		targets = append(targets, *s.Canary)
	}
	for _, t := range targets {
		if !revisionPattern.MatchString(t.Revision) {
			return fmt.Errorf("revision %q must be 1 to 128 characters of letters, digits and : . _ -", t.Revision)
		}
		if _, err := ParseUpstream(t.URL); err != nil {
			return err
		}
	}
	switch {
	case s.Canary == nil && s.CanaryPercent != 0:
		return errors.New("a canary percentage needs a canary")
	case s.Canary != nil && (s.CanaryPercent < 1 || s.CanaryPercent > 99):
		return errors.New("a canary takes 1 to 99 percent of requests; 100 is a new stable")
	case s.Canary != nil && s.Canary.Revision == s.Stable.Revision:
		return errors.New("the canary and the stable are the same revision")
	}
	return nil
}

// Splitter divides requests between a stable and a canary revision and
// counts the outcome of each by revision. The division is exact over every
// hundred requests, not random, so a monitor can rely on the ratio. It is
// safe for concurrent use.
type Splitter struct {
	log      *slog.Logger
	counters *Counters

	mu      sync.Mutex
	split   Split
	acc     int
	proxies map[Target]*httputil.ReverseProxy
}

// NewSplitter starts with all traffic on stable. A nil logger discards.
func NewSplitter(clock core.Clock, log *slog.Logger, stable Target) (*Splitter, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Splitter{log: log, counters: NewCounters(clock), proxies: map[Target]*httputil.ReverseProxy{}}
	if err := s.SetSplit(context.Background(), Split{Stable: stable}); err != nil {
		return nil, err
	}
	return s, nil
}

// Counters returns what the splitter counts, for a monitor to read.
func (s *Splitter) Counters() *Counters { return s.counters }

// Split returns the division in force.
func (s *Splitter) Split(_ context.Context) (Split, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.split, nil
}

// SetSplit replaces the division. A refused split changes nothing.
func (s *Splitter) SetSplit(_ context.Context, next Split) error {
	if err := next.validate(); err != nil {
		return core.NewError(core.CodeInvalidArgument, err.Error())
	}
	next.Stable.URL = normalizeURL(next.Stable.URL)
	if next.Canary != nil {
		c := *next.Canary
		c.URL = normalizeURL(c.URL)
		next.Canary = &c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range []*Target{&next.Stable, next.Canary} {
		if t == nil || s.proxies[*t] != nil {
			continue
		}
		u, _ := ParseUpstream(t.URL) // validated above
		s.proxies[*t] = newProxy(u, s.log, t.Revision)
	}
	s.split, s.acc = next, 0
	return nil
}

// Counts returns what was counted per revision over window, or in total
// when window is zero.
func (s *Splitter) Counts(_ context.Context, window time.Duration) (map[string]Counts, error) {
	if window == 0 {
		return s.counters.Totals(), nil
	}
	return s.counters.Window(window)
}

// pick chooses the target of the next request.
func (s *Splitter) pick() (Target, *httputil.ReverseProxy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.split.Stable
	if s.split.Canary != nil {
		if s.acc += s.split.CanaryPercent; s.acc >= 100 {
			s.acc -= 100
			t = *s.split.Canary
		}
	}
	return t, s.proxies[t]
}

func (s *Splitter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, proxy := s.pick()
	sw := &statusWriter{ResponseWriter: w}
	s.counters.record(target.Revision, Counts{Requests: 1})
	defer func() {
		if sw.status == 0 {
			return
		}
		done := Counts{Samples: 1}
		if sw.status >= 500 {
			done.ServerErrors = 1
		}
		s.counters.record(target.Revision, done)
	}()
	proxy.ServeHTTP(sw, r)
}

// statusWriter remembers the final status of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection for flushing and
// protocol upgrades.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// normalizeURL returns the canonical text of a validated upstream URL, so
// two spellings of one upstream share a proxy.
func normalizeURL(raw string) string {
	u, err := ParseUpstream(raw)
	if err != nil {
		return raw
	}
	return u.String()
}
