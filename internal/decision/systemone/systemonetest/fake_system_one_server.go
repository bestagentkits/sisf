// Package systemonetest is an in-process fake of a System One backend for
// tests. It speaks the adapter's wire protocol on an ephemeral loopback port
// and never calls a real model.
package systemonetest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/bestagentkits/sisf/internal/decision/systemone"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// Call is one request the fake received.
type Call struct {
	Request systemone.WireRequest
	// Authorized is true when the request carried the expected credential.
	Authorized bool
}

// Reply is a raw HTTP answer, for replies the adapter must refuse.
type Reply struct {
	Status      int
	ContentType string
	// Version is sent in the version header.
	Version string
	Body    []byte
}

// Server is the fake backend. Its zero behaviour answers HTTP 503.
type Server struct {
	URL string

	srv      *httptest.Server
	version  string
	token    string
	release  chan struct{}
	closing  sync.Once
	received chan struct{}

	mu      sync.Mutex
	respond func(Call) Reply
	hang    bool
	calls   []Call
}

// New starts a fake that speaks version and, when token is not empty,
// accepts only requests that carry it as a bearer credential.
func New(version, token string) *Server {
	s := &Server{version: version, token: token, release: make(chan struct{}), received: make(chan struct{}, 64)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	return s
}

// Close releases hanging requests and stops the server. It may be called
// more than once.
func (s *Server) Close() {
	s.closing.Do(func() {
		close(s.release)
		s.srv.Close()
	})
}

// Answer makes the fake reply with the judgment fn returns, encoded under
// the fake's version.
func (s *Server) Answer(fn func(pkgdecision.Request) pkgdecision.Judgment) {
	s.Reply(func(c Call) Reply {
		body, _ := json.Marshal(systemone.WireReply{Version: s.version, Judgment: fn(c.Request.Request)})
		return Reply{Status: http.StatusOK, ContentType: "application/json", Version: s.version, Body: body}
	})
}

// Reply makes the fake answer with whatever fn returns.
func (s *Server) Reply(fn func(Call) Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond, s.hang = fn, false
}

// Hang makes the fake hold every request until the caller gives up.
func (s *Server) Hang() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang = true
}

// Received signals each request as it arrives.
func (s *Server) Received() <-chan struct{} { return s.received }

// Calls returns the requests received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	call := Call{Authorized: s.token == "" || r.Header.Get("Authorization") == "Bearer "+s.token}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	decodeErr := dec.Decode(&call.Request)
	s.mu.Lock()
	s.calls = append(s.calls, call)
	respond, hang := s.respond, s.hang
	s.mu.Unlock()
	select {
	case s.received <- struct{}{}:
	default:
	}
	switch {
	case r.Method != http.MethodPost:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	case !call.Authorized:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case r.Header.Get(systemone.HeaderVersion) != s.version || call.Request.Version != s.version:
		http.Error(w, "unsupported version", http.StatusConflict)
	case decodeErr != nil:
		http.Error(w, "bad request", http.StatusBadRequest)
	case hang:
		select {
		case <-r.Context().Done():
		case <-s.release:
		}
	case respond == nil:
		http.Error(w, "no behaviour configured", http.StatusServiceUnavailable)
	default:
		reply := respond(call)
		if reply.ContentType != "" {
			w.Header().Set("Content-Type", reply.ContentType)
		}
		if reply.Version != "" {
			w.Header().Set(systemone.HeaderVersion, reply.Version)
		}
		w.WriteHeader(reply.Status)
		_, _ = bytes.NewReader(reply.Body).WriteTo(w)
	}
}
