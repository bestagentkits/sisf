package fakegithub

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultInstallationID is the installation id webhooks carry unless
// WithInstallationID says otherwise.
const DefaultInstallationID int64 = 4242

// ActorHeader lets a request act as another account, so a standalone fake can
// be driven as a person with plain REST calls. Its value is "login" or
// "login:ASSOCIATION". Without it a request acts as the factory's bot.
const ActorHeader = "X-Sim-Actor"

// Server is the fake GitHub. Create it with New; it is safe for concurrent use.
type Server struct {
	mu             sync.Mutex
	installationID int64
	token          string
	bot            User
	clock          func() time.Time
	tickAt         time.Time
	repos          map[string]*repo
	users          map[string]User
	nextUserID     int64
	nextCommentID  int64
	nextCheckID    int64

	faultMu  sync.Mutex
	faults   []apiFault
	requests []Request

	hooks hooks
	mux   *http.ServeMux
}

// Option configures New.
type Option func(*Server)

// WithInstallationID sets the installation id webhooks carry.
func WithInstallationID(id int64) Option { return func(s *Server) { s.installationID = id } }

// WithToken makes the API accept only this bearer token. Without it, any
// non-empty bearer token is accepted.
func WithToken(token string) Option { return func(s *Server) { s.token = token } }

// WithBot sets the account API calls act as by default: the factory's app bot.
func WithBot(login string, id int64) Option {
	return func(s *Server) { s.bot = User{ID: id, Login: login, Bot: true} }
}

// WithClock replaces the built-in clock. By default time starts at a fixed
// instant and advances one second per change, so timestamps are strictly
// increasing and tests are deterministic.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.clock = now } }

// New returns an empty fake with no repositories and no webhook.
func New(opts ...Option) *Server {
	s := &Server{
		installationID: DefaultInstallationID,
		bot:            User{ID: 900001, Login: "sisf-factory[bot]", Bot: true},
		tickAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		repos:          map[string]*repo{},
		users:          map[string]User{},
		nextUserID:     1000,
		nextCommentID:  5000,
		nextCheckID:    7000,
		mux:            http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.hooks.client.Timeout = 5 * time.Second
	s.routes()
	return s
}

// now returns the time to stamp a change with. Callers hold s.mu.
func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	s.tickAt = s.tickAt.Add(time.Second)
	return s.tickAt
}

// Request is one API call the fake answered.
type Request struct {
	Method string
	Path   string
	Status int
}

// Requests returns the API calls answered so far, oldest first.
func (s *Server) Requests() []Request {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	return append([]Request(nil), s.requests...)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ServeHTTP serves the API and the simulation control endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_sim/") {
		s.mux.ServeHTTP(w, r)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		s.faultMu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Status: rec.status})
		s.faultMu.Unlock()
	}()
	if !s.authorized(r) {
		writeJSON(rec, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	if fault, ok := s.nextFault(); ok {
		fault.write(rec)
		return
	}
	s.mux.ServeHTTP(rec, r)
}

func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	return s.token == "" || token == s.token
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func readJSON(r *http.Request, v any) bool {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v) == nil
}
