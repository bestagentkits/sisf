package github

import "sync"

// deliveryLedger remembers which deliveries were fully handled, so GitHub's
// retries and redeliveries of the same delivery id are acknowledged without
// being processed twice. It is bounded: the oldest ids are forgotten first.
//
// This is only the first line of defence. The inbox the sink writes to
// deduplicates on the delivery id durably, which also covers a restart.
type deliveryLedger struct {
	mu       sync.Mutex
	capacity int
	done     map[string]struct{}
	order    []string
	inflight map[string]struct{}
}

func newDeliveryLedger(capacity int) *deliveryLedger {
	return &deliveryLedger{capacity: capacity, done: map[string]struct{}{}, inflight: map[string]struct{}{}}
}

// begin claims id for handling. It reports duplicate when id was already
// handled, and busy when another request is handling it right now.
func (l *deliveryLedger) begin(id string) (duplicate, busy bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.done[id]; ok {
		return true, false
	}
	if _, ok := l.inflight[id]; ok {
		return false, true
	}
	l.inflight[id] = struct{}{}
	return false, false
}

// finish releases the claim. Only a handled delivery is remembered; a failed
// one stays eligible so that a redelivery is processed.
func (l *deliveryLedger) finish(id string, handled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inflight, id)
	if !handled {
		return
	}
	l.done[id] = struct{}{}
	l.order = append(l.order, id)
	if len(l.order) > l.capacity {
		delete(l.done, l.order[0])
		l.order = l.order[1:]
	}
}
