package actions

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RoleResolver returns the roles an authenticated actor holds right now,
// from GitHub membership and local grants. The rbac authorizer is one.
type RoleResolver interface {
	ResolveRoles(ctx context.Context, actor core.Actor) ([]core.Role, error)
}

// RoleResolverFunc adapts a function to RoleResolver.
type RoleResolverFunc func(ctx context.Context, actor core.Actor) ([]core.Role, error)

// ResolveRoles calls f.
func (f RoleResolverFunc) ResolveRoles(ctx context.Context, actor core.Actor) ([]core.Role, error) {
	return f(ctx, actor)
}

// resolveRoles replaces the roles of the caller with the resolved ones. A
// failure to resolve them is UNAVAILABLE: nothing runs on guessed roles.
func (s *Service) resolveRoles(ctx context.Context, req *core.ActionRequest) error {
	if s.cfg.Roles == nil {
		return nil
	}
	roles, err := s.cfg.Roles.ResolveRoles(ctx, req.Actor)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the caller's roles could not be resolved").WithCause(err)
	}
	req.Actor.Roles = roles
	return nil
}

// Defaults of the write rate limit.
const (
	DefaultWriteRate       = 60
	DefaultWriteRateWindow = time.Minute
	maxLimitedActors       = 4096
)

// RateLimit bounds the writes of one actor: at most Writes calls that change
// something in each Window. Reads and dry runs are not counted; they change
// nothing and cost nothing. It holds in every budget mode, so a looping
// agent or script cannot run up work however much money is allowed.
type RateLimit struct {
	Writes int
	Window time.Duration
}

// writeLimiter counts writes per actor in fixed windows.
type writeLimiter struct {
	limit RateLimit
	mu    sync.Mutex
	by    map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newWriteLimiter(limit RateLimit) *writeLimiter {
	if limit.Writes == 0 {
		limit.Writes = DefaultWriteRate
	}
	if limit.Window == 0 {
		limit.Window = DefaultWriteRateWindow
	}
	return &writeLimiter{limit: limit, by: map[string]*window{}}
}

// limitWrites refuses a write beyond the actor's limit with RATE_LIMITED and
// the time to wait. A fallback, a read and a dry run are never counted.
func (s *Service) limitWrites(call invocation) error {
	if !call.spec.Mutates() || call.req.DryRun || s.cfg.Registry.IsFallback(call.spec.Name) {
		return nil
	}
	wait, ok := s.limiter.take(call.req.Actor.ID, s.cfg.Clock.Now())
	if ok {
		return nil
	}
	e := core.NewError(core.CodeRateLimited, "too many writes by this caller; wait before trying again")
	e.Details = map[string]string{"retry_after_seconds": strconv.Itoa(int(wait.Round(time.Second) / time.Second))}
	return e
}

// take counts one write of actor at now, or reports how long to wait.
func (l *writeLimiter) take(actor string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.by[actor]
	if !ok || !now.Before(w.start.Add(l.limit.Window)) || now.Before(w.start) {
		if !ok && len(l.by) >= maxLimitedActors {
			l.pruneLocked(now)
		}
		w = &window{start: now}
		l.by[actor] = w
	}
	if w.count >= l.limit.Writes {
		return w.start.Add(l.limit.Window).Sub(now), false
	}
	w.count++
	return 0, true
}

// pruneLocked drops the windows that have ended, so memory stays bounded.
func (l *writeLimiter) pruneLocked(now time.Time) {
	for actor, w := range l.by {
		if !now.Before(w.start.Add(l.limit.Window)) {
			delete(l.by, actor)
		}
	}
}
