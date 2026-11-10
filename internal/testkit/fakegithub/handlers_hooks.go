package fakegithub

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// listDeliveries answers GitHub's "list deliveries for an app webhook".
func (s *Server) listDeliveries(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, d := range s.Deliveries() {
		out = append(out, map[string]any{
			"id": d.Seq, "guid": d.ID, "event": d.Event, "action": d.Action,
			"status_code": d.Status, "redelivery": d.Attempts > 1, "dropped": d.Dropped,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// redeliver answers GitHub's "redeliver a delivery for an app webhook".
func (s *Server) redeliver(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	var guid string
	for _, d := range s.Deliveries() {
		if d.Seq == seq {
			guid = d.ID
		}
	}
	if guid == "" {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	if err := s.Redeliver(guid); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("redelivery failed: %v", err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// The control endpoints let a process that only speaks HTTP, such as the
// standalone simulation, set the fake up and inject faults.

func (s *Server) simRepo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo string `json:"repo"`
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusBadRequest, "expected {\"repo\":\"owner/name\"}")
		return
	}
	if err := s.AddRepo(in.Repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) simWebhook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if !readJSON(r, &in) {
		writeError(w, http.StatusBadRequest, "expected {\"url\":...,\"secret\":...}")
		return
	}
	s.SetWebhook(in.URL, in.Secret)
	w.WriteHeader(http.StatusNoContent)
}

// simFaults injects webhook faults: drop and duplicate count the next
// deliveries, hold queues them, and release ("ordered" or "reversed") sends
// the queue.
func (s *Server) simFaults(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Drop      int    `json:"drop"`
		Duplicate int    `json:"duplicate"`
		Hold      bool   `json:"hold"`
		Release   string `json:"release"`
		Limit     int    `json:"rate_limit"`
	}
	if !readJSON(r, &in) || (in.Release != "" && in.Release != "ordered" && in.Release != "reversed") {
		writeError(w, http.StatusBadRequest, "release must be ordered or reversed")
		return
	}
	s.DropNext(in.Drop)
	s.DuplicateNext(in.Duplicate)
	if in.Limit > 0 {
		s.LimitNext(in.Limit, time.Second)
	}
	if in.Hold {
		s.Hold()
	}
	if in.Release != "" {
		s.Release(in.Release == "reversed")
	}
	w.WriteHeader(http.StatusNoContent)
}
