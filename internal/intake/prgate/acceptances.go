package prgate

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Acceptance records that a Maintainer let one revision of a pull request
// into the pipeline. It covers HeadSHA only; a new push needs a new one.
type Acceptance struct {
	Repo    github.RepoRef `json:"repo"`
	Number  int            `json:"number"`
	HeadSHA core.CommitSHA `json:"head_sha"`
	ActorID string         `json:"actor_id"`
	At      time.Time      `json:"at"`
}

// AcceptanceStore keeps the latest acceptance of each pull request.
type AcceptanceStore interface {
	Put(ctx context.Context, a Acceptance) error
	Get(ctx context.Context, repo github.RepoRef, number int) (Acceptance, bool, error)
}

// MemoryAcceptances is an in-process AcceptanceStore. It is safe for
// concurrent use.
type MemoryAcceptances struct {
	mu sync.Mutex
	m  map[acceptanceKey]Acceptance
}

type acceptanceKey struct {
	repo   github.RepoRef
	number int
}

var _ AcceptanceStore = (*MemoryAcceptances)(nil)

// NewMemoryAcceptances returns an empty store.
func NewMemoryAcceptances() *MemoryAcceptances {
	return &MemoryAcceptances{m: map[acceptanceKey]Acceptance{}}
}

// Put replaces the acceptance of the pull request.
func (s *MemoryAcceptances) Put(_ context.Context, a Acceptance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[acceptanceKey{a.Repo, a.Number}] = a
	return nil
}

// Get returns the acceptance of the pull request, if any.
func (s *MemoryAcceptances) Get(_ context.Context, repo github.RepoRef, number int) (Acceptance, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[acceptanceKey{repo, number}]
	return a, ok, nil
}
