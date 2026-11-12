package auth

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// flowTable is a service holding sign-in flows only, choosing which to push
// out with rng, without the start limits: its tests are about the table.
func flowTable(rng *rand.Rand) *Service {
	return &Service{flows: map[string]*flow{}, pick: func(n int) (int, error) { return rng.IntN(n), nil },
		starts: report.NewStartLimits(1<<30, 1<<30, time.Minute, 1<<20)}
}

// admit starts a flow from source at now as beginFlow does and returns its
// key, or "" when it was refused.
func (s *Service) admit(now time.Time, source string, protected bool) string {
	if s.pruneFlowsLocked(now, source, "", protected) != nil {
		return ""
	}
	s.flowSeq++
	key := fmt.Sprint(s.flowSeq)
	s.flows[key] = &flow{source: source, seq: s.flowSeq, started: now, expires: now.Add(flowTTL), protected: protected}
	return key
}

// A flood of one sign-in from each of about 1,024 addresses, renewed at
// about 1.7 starts a second, keeps the table full of flows from sources
// holding one each. Every start is admitted, a protected flow is never
// pushed out, and a person's sign-in finished within a minute of its start
// survives in at least 85% of cases: people start every other sign-in of
// the flood, and each is checked a minute after it started. The table is
// filled at the start and the test ends before those flows lapse, so every
// start pushes a flow out, the worst case of that rate.
func TestAOnePerSourceFloodRarelyEvictsAPersonsSignIn(t *testing.T) {
	t.Parallel()
	const people = 400
	interval := time.Second * 10 / 17 // 1.7 starts a second
	perMinute := int(time.Minute / interval)
	s := flowTable(rand.New(rand.NewPCG(1, 2)))
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	protected := s.admit(now, "owner", true)
	for len(s.flows) < maxFlows {
		// The table is not full yet, so nothing is pushed out: the flows are
		// put in place directly.
		s.flowSeq++
		s.flows[fmt.Sprint(s.flowSeq)] = &flow{source: fmt.Sprintf("fill-%d", s.flowSeq), seq: s.flowSeq, expires: now.Add(flowTTL)}
	}
	type person struct {
		key     string
		started int
	}
	var waiting []person
	survived, checked := 0, 0
	for start := 0; checked < people; start++ {
		now = now.Add(interval)
		source, isPerson := fmt.Sprintf("flood-%d", start), start%2 == 0
		if isPerson {
			source = fmt.Sprintf("person-%d", start)
		}
		key := s.admit(now, source, false)
		if key == "" {
			t.Fatalf("start %d was refused with the table full of one flow per source", start)
		}
		if isPerson {
			waiting = append(waiting, person{key: key, started: start})
		}
		for len(waiting) > 0 && start-waiting[0].started >= perMinute {
			if _, ok := s.flows[waiting[0].key]; ok {
				survived++
			}
			checked++
			waiting = waiting[1:]
		}
		if _, ok := s.flows[protected]; !ok {
			t.Fatalf("start %d pushed out a protected flow", start)
		}
		if f := s.flows[protected]; !now.Add(interval).Before(f.expires) {
			// It lapses with the next start; the Owner starts another.
			delete(s.flows, protected)
			protected = s.admit(now, "owner", true)
		}
	}
	t.Logf("%d of %d people's sign-ins survived a minute of the flood", survived, people)
	if elapsed := now.Sub(time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)); elapsed >= flowTTL {
		t.Fatalf("the flood ran %s, past the flows' lifetime, so not every start pushed one out", elapsed)
	}
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's sign-ins survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// A source holding more than one flow still loses its own oldest first, so
// a flood from a few addresses never reaches a person's single sign-in.
func TestTheBusiestSourceLosesItsOldestFlowFirst(t *testing.T) {
	t.Parallel()
	s := flowTable(rand.New(rand.NewPCG(3, 4)))
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	person := s.admit(now, "person", false)
	s.admit(now, "busy", false)
	s.admit(now, "busy", false)
	for i := 0; len(s.flows) < maxFlows; i++ {
		s.admit(now, fmt.Sprintf("single-%d", i), false)
	}
	for range 3 * maxFlows {
		if s.admit(now, "busy", false) == "" {
			t.Fatal("a start from a busy source was refused")
		}
	}
	if _, ok := s.flows[person]; !ok {
		t.Fatal("a busy source pushed out the person's single sign-in")
	}
}

// A start into a table with nothing it may push out, every flow protected,
// is refused without spending a random eviction, and says when to come
// back: when the first open flow lapses.
func TestAStartWithNothingToPushOutSpendsNoEviction(t *testing.T) {
	t.Parallel()
	s := flowTable(rand.New(rand.NewPCG(5, 6)))
	s.starts = report.NewStartLimits(1<<30, 1, time.Minute, 1<<20)
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	for i := range maxFlows {
		s.admit(now.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("owner-%d", i), true)
	}
	later := now.Add(4 * time.Minute)
	err := s.pruneFlowsLocked(later, "newcomer", "", false)
	var typed *core.Error
	if !errors.As(err, &typed) || typed.Code != core.CodeRateLimited || typed.Details["retry_after_seconds"] != "360" {
		t.Fatalf("a start with every flow protected: %v, want RATE_LIMITED until the first flow lapses in 360 seconds", err)
	}
	if ok, _ := s.starts.Evict(later); !ok {
		t.Fatal("a refused start spent the random eviction it never made")
	}
}
