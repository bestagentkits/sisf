package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// userCodeAlphabet has 32 symbols, no I or O, so a byte masked to five
	// bits picks one uniformly and a code is easy to read aloud.
	userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	userCodeLen      = 8
	maxOpenGrants    = 256
)

// grant is one login request. The device code stays with the CLI that
// started it; the user code is what the person types or confirms.
type grant struct {
	userCode string
	name     string
	scopes   []string
	expires  time.Time
	lastPoll time.Time
	// claimed is set while a person's approval is being processed, so two
	// approvals of one code cannot both mint a token.
	claimed  bool
	approved bool
	token    core.SecretValue
	info     auth.TokenInfo
}

// deviceStore holds the open login requests in memory: a restart voids them,
// which is the safe failure.
type deviceStore struct {
	clock    core.Clock
	ttl      time.Duration
	interval time.Duration

	mu     sync.Mutex
	grants map[string]*grant // by digest of the device code
	byUser map[string]string // user code to digest of the device code
}

func newDeviceStore(clock core.Clock, ttl, interval time.Duration) *deviceStore {
	return &deviceStore{clock: clock, ttl: ttl, interval: interval, grants: map[string]*grant{}, byUser: map[string]string{}}
}

func digestOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// normalizeUserCode accepts the code as typed: any case, with dashes or spaces.
func normalizeUserCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
}

// formatUserCode shows the code as two groups, "ABCD-EFGH".
func formatUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:userCodeLen/2] + "-" + code[userCodeLen/2:]
}

func randomUserCode() (string, error) {
	raw := make([]byte, userCodeLen)
	if _, err := rand.Read(raw); err != nil {
		return "", core.NewError(core.CodeInternal, "no randomness available").WithCause(err)
	}
	for i, b := range raw {
		raw[i] = userCodeAlphabet[b&31]
	}
	return string(raw), nil
}

func (s *deviceStore) dropLocked(key string) {
	if g, ok := s.grants[key]; ok {
		delete(s.byUser, g.userCode)
		delete(s.grants, key)
	}
}

// purgeLocked forgets every expired grant except one whose approval is in
// flight, which always ends in approve or release.
func (s *deviceStore) purgeLocked(now time.Time) {
	for key, g := range s.grants {
		if !g.claimed && !now.Before(g.expires) {
			s.dropLocked(key)
		}
	}
}

// start opens a login request and returns its device code.
func (s *deviceStore) start(name string, scopes []string) (deviceCode string, g grant, err error) {
	raw := make([]byte, 32)
	if _, rerr := rand.Read(raw); rerr != nil {
		return "", g, core.NewError(core.CodeInternal, "no randomness available").WithCause(rerr)
	}
	deviceCode = base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.purgeLocked(now)
	if len(s.grants) >= maxOpenGrants {
		return "", g, core.NewError(core.CodeRateLimited, "too many login requests are open; try again shortly")
	}
	var userCode string
	for range 8 {
		if userCode, err = randomUserCode(); err != nil {
			return "", g, err
		}
		if _, taken := s.byUser[userCode]; !taken {
			break
		}
		userCode = ""
	}
	if userCode == "" {
		return "", g, core.NewError(core.CodeInternal, "no free login code")
	}
	open := &grant{userCode: userCode, name: name, scopes: slices.Clone(scopes), expires: now.Add(s.ttl)}
	s.grants[digestOf(deviceCode)] = open
	s.byUser[userCode] = digestOf(deviceCode)
	return deviceCode, *open, nil
}

// poll answers the CLI. It returns the token exactly once, then forgets the
// request. pending reports a request nobody approved yet.
func (s *deviceStore) poll(deviceCode string) (token core.SecretValue, info auth.TokenInfo, pending bool, err error) {
	key := digestOf(deviceCode)
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[key]
	now := s.clock.Now()
	switch {
	case !ok:
		return token, info, false, core.NewError(core.CodeNotFound, "no such login request; it may have been used already")
	case !now.Before(g.expires) && !g.claimed:
		s.dropLocked(key)
		return token, info, false, core.NewError(core.CodeExpired, "the login request expired; start again")
	case !g.lastPoll.IsZero() && now.Sub(g.lastPoll) < s.interval:
		wait := (s.interval - now.Sub(g.lastPoll) + time.Second - 1) / time.Second
		e := core.NewError(core.CodeRateLimited, "polling too fast; wait for the interval")
		e.Details = map[string]string{"retry_after_seconds": strconv.FormatInt(int64(wait), 10)}
		return token, info, false, e
	}
	g.lastPoll = now
	if !g.approved {
		return token, info, true, nil
	}
	token, info = g.token, g.info
	s.dropLocked(key)
	return token, info, false, nil
}

// lookup returns a copy of the open request behind a user code, for the
// verification page.
func (s *deviceStore) lookup(userCode string) (grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, g, err := s.findLocked(normalizeUserCode(userCode))
	if err != nil {
		return grant{}, err
	}
	return grant{userCode: g.userCode, name: g.name, scopes: slices.Clone(g.scopes), expires: g.expires, approved: g.approved}, nil
}

func (s *deviceStore) findLocked(userCode string) (string, *grant, error) {
	key, ok := s.byUser[userCode]
	if !ok {
		return "", nil, core.NewError(core.CodeNotFound, "no login request has this code")
	}
	g := s.grants[key]
	if !g.claimed && !s.clock.Now().Before(g.expires) {
		s.dropLocked(key)
		return "", nil, core.NewError(core.CodeExpired, "the login request expired; start again in the terminal")
	}
	return key, g, nil
}

// claim reserves a pending request for one approval and returns what the
// token must carry. A request approved or being approved cannot be claimed.
func (s *deviceStore) claim(userCode string) (key string, g grant, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, open, err := s.findLocked(normalizeUserCode(userCode))
	if err != nil {
		return "", g, err
	}
	if open.approved || open.claimed {
		return "", g, core.NewError(core.CodeReplayed, "this login code was already approved")
	}
	open.claimed = true
	return key, grant{userCode: open.userCode, name: open.name, scopes: slices.Clone(open.scopes), expires: open.expires}, nil
}

// release gives a claim back after the token could not be minted.
func (s *deviceStore) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.grants[key]; ok {
		g.claimed = false
	}
}

// approve records the minted token for the CLI to collect.
func (s *deviceStore) approve(key string, issued auth.IssuedToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.grants[key]; ok {
		g.claimed, g.approved, g.token, g.info = false, true, issued.Token, issued.TokenInfo
	}
}
