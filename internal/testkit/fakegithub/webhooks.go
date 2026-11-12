package fakegithub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
)

// Delivery is one webhook the fake produced. Redelivering it reuses its
// GUID, as GitHub does.
type Delivery struct {
	Seq      int
	ID       string
	Event    string
	Action   string
	Payload  []byte
	Attempts int
	// Status is the HTTP status of the last attempt, 0 when none was made or
	// the receiver could not be reached.
	Status int
	// Dropped is true while no attempt has been made because a drop was
	// injected. A redelivery clears it.
	Dropped bool
}

// hooks holds the webhook target and the injection state.
type hooks struct {
	mu         sync.Mutex
	url        string
	secret     string
	deliveries []*Delivery
	held       []*Delivery
	holding    bool
	dropNext   int
	dupNext    int
	client     http.Client
}

// pending is an event produced by a change, delivered once the change is
// complete and no lock is held.
type pending struct {
	event, action string
	payload       []byte
}

// SetWebhook points deliveries at url and signs them with secret. An empty
// url turns delivery off.
func (s *Server) SetWebhook(url, secret string) {
	s.hooks.mu.Lock()
	defer s.hooks.mu.Unlock()
	s.hooks.url, s.hooks.secret = url, secret
}

// DropNext records the next n deliveries without sending them, as if the
// network lost them. Redeliver or the redelivery API recovers them.
func (s *Server) DropNext(n int) { s.hooks.mu.Lock(); s.hooks.dropNext += n; s.hooks.mu.Unlock() }

// DuplicateNext sends each of the next n deliveries twice.
func (s *Server) DuplicateNext(n int) { s.hooks.mu.Lock(); s.hooks.dupNext += n; s.hooks.mu.Unlock() }

// Hold queues deliveries instead of sending them, until Release.
func (s *Server) Hold() { s.hooks.mu.Lock(); s.hooks.holding = true; s.hooks.mu.Unlock() }

// Release stops holding and sends the queued deliveries, oldest first, or
// newest first when reversed is true: the way to reorder events.
func (s *Server) Release(reversed bool) {
	s.hooks.mu.Lock()
	queued := s.hooks.held
	s.hooks.held, s.hooks.holding = nil, false
	s.hooks.mu.Unlock()
	if reversed {
		slices.Reverse(queued)
	}
	for _, d := range queued {
		_ = s.send(d) // the outcome is recorded on the delivery
	}
}

// Deliveries returns copies of every delivery produced so far.
func (s *Server) Deliveries() []Delivery {
	s.hooks.mu.Lock()
	defer s.hooks.mu.Unlock()
	out := make([]Delivery, len(s.hooks.deliveries))
	for i, d := range s.hooks.deliveries {
		out[i] = *d
	}
	return out
}

// Redeliver sends a recorded delivery again with its original GUID.
func (s *Server) Redeliver(id string) error {
	s.hooks.mu.Lock()
	var found *Delivery
	for _, d := range s.hooks.deliveries {
		if d.ID == id {
			found = d
		}
	}
	s.hooks.mu.Unlock()
	if found == nil {
		return fmt.Errorf("no delivery %q", id)
	}
	return s.send(found)
}

// dispatch turns the events of one change into deliveries.
func (s *Server) dispatch(events []pending) {
	s.hooks.mu.Lock()
	if s.hooks.url == "" {
		s.hooks.mu.Unlock()
		return
	}
	var toSend []*Delivery
	var dups []bool
	for _, e := range events {
		seq := len(s.hooks.deliveries) + 1
		d := &Delivery{Seq: seq, ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", seq), Event: e.event, Action: e.action, Payload: e.payload}
		s.hooks.deliveries = append(s.hooks.deliveries, d)
		switch {
		case s.hooks.dropNext > 0:
			s.hooks.dropNext--
			d.Dropped = true
		case s.hooks.holding:
			s.hooks.held = append(s.hooks.held, d)
		default:
			dup := s.hooks.dupNext > 0
			if dup {
				s.hooks.dupNext--
			}
			toSend, dups = append(toSend, d), append(dups, dup)
		}
	}
	s.hooks.mu.Unlock()
	for i, d := range toSend {
		_ = s.send(d)
		if dups[i] {
			_ = s.send(d)
		}
	}
}

// send posts a delivery to the target and records the outcome.
func (s *Server) send(d *Delivery) error {
	s.hooks.mu.Lock()
	url, secret, client := s.hooks.url, s.hooks.secret, &s.hooks.client
	payload := d.Payload
	d.Dropped = false
	s.hooks.mu.Unlock()
	if url == "" {
		return fmt.Errorf("no webhook target is configured")
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", d.Event)
	req.Header.Set("X-GitHub-Delivery", d.ID)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	status := 0
	resp, err := client.Do(req)
	if err == nil {
		status = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	s.hooks.mu.Lock()
	d.Attempts++
	d.Status = status
	s.hooks.mu.Unlock()
	return err
}

// event builds the payload common to every webhook and adds the fields.
func (s *Server) event(r *repo, eventType, action string, sender User, fields map[string]any) pending {
	body := map[string]any{
		"action":       action,
		"installation": map[string]any{"id": s.installationID},
		"repository":   repoJSON(r.owner, r.name),
		"sender":       sender.json(),
	}
	for k, v := range fields {
		body[k] = v
	}
	raw, _ := json.Marshal(body) // plain data; cannot fail
	return pending{event: eventType, action: action, payload: raw}
}
