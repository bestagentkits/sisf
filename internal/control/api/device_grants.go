package api

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// userCodeAlphabet has 32 symbols, no I or O, so a byte masked to five
	// bits picks one uniformly and a code is easy to read aloud.
	userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	userCodeLen      = 8
	// maxOpenGrants bounds the open login requests. When every slot is
	// held by a source with one, a new start drops one chosen at random,
	// so the table is sized for a person's request to outlast a flood: at
	// 1.7 starts a second, one approved within a minute survives about
	// nine times in ten.
	maxOpenGrants = 1024
)

// grant is one login request. The device code stays with the CLI that
// started it; the user code is what the person types or confirms.
type grant struct {
	userCode string
	name     string
	scopes   []string
	expires  time.Time
	lastPoll time.Time
	// source is the network source that started the request and seq the
	// order requests were started in.
	source string
	seq    uint64
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
	seq    uint64
	// pick chooses which of n waiting requests to drop when every source
	// holds one; nil means randomIndex.
	pick func(n int) (int, error)
	// starts limits how often login requests are started (admitStart).
	starts *report.Limiter
}

func newDeviceStore(clock core.Clock, ttl, interval time.Duration) *deviceStore {
	return &deviceStore{clock: clock, ttl: ttl, interval: interval, grants: map[string]*grant{}, byUser: map[string]string{},
		starts: report.NewStartLimiter(maxOpenGrants)}
}

// admitStart takes a login request from source off the start limits
// (report.NewStartLimiter), before start: StartsPerSource a minute from
// one source and, from all together, the rate at which a flood rotating
// through new sources drops a person's request approved within a minute
// with probability at most 15%. A start beyond them is refused with
// RATE_LIMITED and the seconds until it may be tried again.
func (s *deviceStore) admitStart(source string) error {
	ok, wait := s.starts.Take(source, s.clock.Now())
	if ok {
		return nil
	}
	err := core.NewError(core.CodeRateLimited, "too many login requests are being started; try again shortly")
	err.Details = map[string]string{"retry_after_seconds": report.RetryAfter(wait)}
	return err
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

// randomIndex returns a uniformly random index below n from crypto/rand.
func randomIndex(n int) (int, error) {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, core.NewError(core.CodeInternal, "no randomness available").WithCause(err)
	}
	return int(i.Int64()), nil
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

// start opens a login request from source and returns its device code.
// Anyone may start one, so while all maxOpenGrants are taken it drops the
// oldest waiting request of the source holding the most, as long as that
// source holds more than one: a flood from another address, or from a few,
// does not reach a person's request, and one sharing the person's address
// takes as many starts as there are slots. When every source holds one, it
// drops a waiting request chosen at random (crypto/rand): a flood of one
// start from each of many addresses neither locks out a new start nor aims
// at a person's request. A request being approved, or approved and not yet
// collected, is never dropped; start is refused only when every open
// request is one of those.
func (s *deviceStore) start(name string, scopes []string, source string) (deviceCode string, g grant, err error) {
	raw := make([]byte, 32)
	if _, rerr := rand.Read(raw); rerr != nil {
		return "", g, core.NewError(core.CodeInternal, "no randomness available").WithCause(rerr)
	}
	deviceCode = base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.purgeLocked(now)
	if !s.makeRoomLocked(source) {
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
	s.seq++
	open := &grant{userCode: userCode, name: name, scopes: slices.Clone(scopes), expires: now.Add(s.ttl), source: source, seq: s.seq}
	s.grants[digestOf(deviceCode)] = open
	s.byUser[userCode] = digestOf(deviceCode)
	return deviceCode, *open, nil
}

// waiting reports whether g waits for a person's approval: neither claimed
// for one nor approved. Only such a request is dropped to make room.
func (g *grant) waiting() bool { return !g.claimed && !g.approved }

// makeRoomLocked drops waiting requests until fewer than maxOpenGrants are
// open for a request from source, the oldest of the source holding the
// most while one holds more than one and a random one otherwise, and
// reports whether there is room. The newcomer's source is counted as if
// its new request were in the table already, so a source that holds a
// request loses its own oldest before any other source's is touched.
func (s *deviceStore) makeRoomLocked(source string) bool {
	perSource := map[string]int{source: 1}
	for _, g := range s.grants {
		if g.waiting() {
			perSource[g.source]++
		}
	}
	for len(s.grants) >= maxOpenGrants {
		busiest := 0
		for _, n := range perSource {
			busiest = max(busiest, n)
		}
		switch {
		case busiest > 1:
			s.dropOldestWaitingLocked(perSource, func(g *grant) bool { return perSource[g.source] == busiest })
		case busiest == 1:
			if !s.dropRandomWaitingLocked(perSource) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// dropRandomWaitingLocked drops a waiting request chosen at random,
// keeping perSource current, and reports whether one was dropped; without
// randomness it drops none.
func (s *deviceStore) dropRandomWaitingLocked(perSource map[string]int) bool {
	type candidate struct {
		key string
		g   *grant
	}
	candidates := make([]candidate, 0, len(s.grants))
	for k, g := range s.grants {
		if g.waiting() {
			candidates = append(candidates, candidate{k, g})
		}
	}
	if len(candidates) == 0 {
		return false
	}
	// Map order is neither uniform nor repeatable: the candidates are put
	// in the order they started, so the choice is pick's alone.
	slices.SortFunc(candidates, func(a, b candidate) int { return cmp.Compare(a.g.seq, b.g.seq) })
	pick := s.pick
	if pick == nil {
		pick = randomIndex
	}
	i, err := pick(len(candidates))
	if err != nil || i < 0 || i >= len(candidates) {
		return false
	}
	s.dropLocked(candidates[i].key)
	perSource[candidates[i].g.source]--
	return true
}

// dropOldestWaitingLocked drops the oldest waiting request among those
// eligible picks, keeping perSource, the count of waiting requests by
// source, current.
func (s *deviceStore) dropOldestWaitingLocked(perSource map[string]int, eligible func(*grant) bool) {
	var key string
	var victim *grant
	for k, g := range s.grants {
		if g.waiting() && eligible(g) && (victim == nil || g.seq < victim.seq) {
			key, victim = k, g
		}
	}
	if victim == nil {
		return
	}
	s.dropLocked(key)
	perSource[victim.source]--
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
