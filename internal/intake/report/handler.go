// Package report is the public endpoint through which users of the
// application report a problem. The endpoint is open to the internet, so it
// limits how often each client and everyone together may report, how large
// a report may be, and stores a report seen again only once.
//
// A report is data. Its invisible characters are dropped, then its text is
// redacted, normalized for deduplication and stored; nothing in it is interpreted, obeyed or echoed back, and storing
// it grants nothing to anyone.
//
// The handler is not mounted here: the wiring of the control zone mounts it
// at Path and exposes that path through the edge.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Path is where the handler is meant to be mounted.
const Path = "/report"

// Defaults for the limits a Config leaves at zero. A client may report five
// times an hour and everyone together sixty times an hour: what is accepted
// waits in a bounded store for the publisher, which opens at most a daily
// budget of issues, so acceptance is kept near what can be published.
const (
	DefaultMaxBytes       = 16 << 10
	DefaultPerClientLimit = 5
	DefaultGlobalLimit    = 60
	DefaultWindow         = time.Hour
	DefaultMaxClients     = 10000
	// MaxTitleRunes bounds the title of a report.
	MaxTitleRunes = 200
	// fullStoreRetry is the Retry-After of a report refused because the
	// store is full: stored reports leave it as they are published, a few
	// a minute while the day's budget of issues lasts.
	fullStoreRetry = 5 * time.Minute
)

// Config configures the handler.
type Config struct {
	Store    Store
	Redactor telemetry.Redactor
	// Clock drives the rate-limit windows and stamps reports. Nil means the
	// wall clock.
	Clock core.Clock
	// MaxBytes bounds the request body.
	MaxBytes int64
	// PerClientLimit and GlobalLimit bound the reports accepted per Window
	// from one client and from all clients together.
	PerClientLimit int
	GlobalLimit    int
	Window         time.Duration
	// MaxClients bounds how many clients are tracked at once.
	MaxClients int
	// ClientKey names the client of a request. Nil means the remote IP
	// address. Behind the edge every request comes from the edge, so the
	// wiring passes a key derived from the address the edge forwards.
	ClientKey func(*http.Request) string
	// Logger receives one line per failed store call, never content. Nil
	// discards them.
	Logger *slog.Logger
}

type handler struct {
	cfg     Config
	limiter *limiter
}

// NewHandler validates cfg, fills in the defaults and returns the handler.
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Store == nil || cfg.Redactor == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the report endpoint needs a store and a redactor")
	}
	if cfg.MaxBytes < 0 || cfg.PerClientLimit < 0 || cfg.GlobalLimit < 0 || cfg.Window < 0 || cfg.MaxClients < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "report limits must not be negative")
	}
	cfg.MaxBytes = orDefault(cfg.MaxBytes, DefaultMaxBytes)
	cfg.PerClientLimit = orDefault(cfg.PerClientLimit, DefaultPerClientLimit)
	cfg.GlobalLimit = orDefault(cfg.GlobalLimit, DefaultGlobalLimit)
	cfg.Window = orDefault(cfg.Window, DefaultWindow)
	cfg.MaxClients = orDefault(cfg.MaxClients, DefaultMaxClients)
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.ClientKey == nil {
		cfg.ClientKey = remoteIP
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &handler{cfg: cfg, limiter: &limiter{
		perClient: cfg.PerClientLimit, overall: cfg.GlobalLimit, window: cfg.Window,
		maxClients: cfg.MaxClients, clients: map[string]*counter{},
	}}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		reply(w, http.StatusMethodNotAllowed, "error", "only POST is accepted")
		return
	}
	now := h.cfg.Clock.Now()
	client := h.cfg.ClientKey(r)
	if ok, wait := h.limiter.take(client, now); !ok {
		w.Header().Set("Retry-After", retryAfter(wait))
		reply(w, http.StatusTooManyRequests, "error", "too many reports; try again later")
		return
	}
	in, status, message := decode(w, r, h.cfg.MaxBytes)
	if status != 0 {
		reply(w, status, "error", message)
		return
	}
	visibleTitle, visibleBody := Visible(in.Title), Visible(in.Body)
	if strings.TrimSpace(visibleTitle) == "" || strings.TrimSpace(visibleBody) == "" {
		reply(w, http.StatusBadRequest, "error", "a report needs a title of at most 200 characters and a body, as printable text")
		return
	}
	title, body := h.cfg.Redactor.Redact(visibleTitle), h.cfg.Redactor.Redact(visibleBody)
	fp := fingerprint(title, body)
	stored, created, err := h.cfg.Store.Save(r.Context(), Report{
		ID: "report-" + fp[:16], Fingerprint: fp, Title: title, Body: body, ReceivedAt: now, Client: client,
	})
	if core.HasCode(err, core.CodeRateLimited) {
		// The store holds as many of this client's reports as it lets one
		// client have waiting.
		w.Header().Set("Retry-After", storeRetryAfter(err))
		reply(w, http.StatusTooManyRequests, "error", "too many of your reports wait to be published; try again later")
		return
	}
	if err != nil {
		h.cfg.Logger.Error("report could not be stored", "code", string(core.CodeOf(err)))
		if core.HasCode(err, core.CodeLimitExceeded) {
			// The store is full until reports are published or lapse.
			w.Header().Set("Retry-After", retryAfter(fullStoreRetry))
		}
		reply(w, http.StatusServiceUnavailable, "error", "the report could not be stored; try again later")
		return
	}
	if !created {
		reply(w, http.StatusOK, "duplicate", stored.ID)
		return
	}
	reply(w, http.StatusAccepted, "accepted", stored.ID)
}

// Visible drops from text the control characters other than line breaks
// and tabs, and every character that shows nothing (Invisible). Such a
// character could split a secret to slip it past redaction and then be
// dropped where the text is shown, joining the secret again: the text is
// made visible before it is redacted and fingerprinted, and so stored and
// published as a reader sees it.
func Visible(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r) || Invisible(r):
			return -1
		}
		return r
	}, text)
}

// invisibleRanges are the code points that show nothing beyond the format
// characters: Unicode's Default_Ignorable_Code_Point, such as the soft
// hyphen, the Hangul fillers, the variation selectors and the tags, and
// the braille blank, which shows as empty space.
var invisibleRanges = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00AD, Hi: 0x00AD, Stride: 1},
		{Lo: 0x034F, Hi: 0x034F, Stride: 1},
		{Lo: 0x061C, Hi: 0x061C, Stride: 1},
		{Lo: 0x115F, Hi: 0x1160, Stride: 1},
		{Lo: 0x17B4, Hi: 0x17B5, Stride: 1},
		{Lo: 0x180B, Hi: 0x180F, Stride: 1},
		{Lo: 0x200B, Hi: 0x200F, Stride: 1},
		{Lo: 0x202A, Hi: 0x202E, Stride: 1},
		{Lo: 0x2060, Hi: 0x206F, Stride: 1},
		{Lo: 0x2800, Hi: 0x2800, Stride: 1},
		{Lo: 0x3164, Hi: 0x3164, Stride: 1},
		{Lo: 0xFE00, Hi: 0xFE0F, Stride: 1},
		{Lo: 0xFEFF, Hi: 0xFEFF, Stride: 1},
		{Lo: 0xFFA0, Hi: 0xFFA0, Stride: 1},
		{Lo: 0xFFF0, Hi: 0xFFF8, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x1BCA0, Hi: 0x1BCA3, Stride: 1},
		{Lo: 0x1D173, Hi: 0x1D17A, Stride: 1},
		{Lo: 0xE0000, Hi: 0xE0FFF, Stride: 1},
	},
	LatinOffset: 1,
}

// Invisible reports whether r shows nothing where text is read: a format
// character (Unicode category Cf, which holds the zero-width space and the
// byte order mark) or another code point of invisibleRanges. Control
// characters are not counted; each caller decides which of them to keep.
func Invisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) || unicode.Is(invisibleRanges, r)
}

// fingerprint hashes the content without regard to letter case or spacing.
func fingerprint(title, body string) string {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	sum := sha256.Sum256([]byte(norm(title) + "\x00" + norm(body)))
	return hex.EncodeToString(sum[:])
}

// reply writes a small JSON answer. For success the detail is the report
// ID; nothing from the request is ever written back.
func reply(w http.ResponseWriter, status int, state, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	key := "id"
	if state == "error" {
		key = "message"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state, key: detail})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// storeRetryAfter is the Retry-After of a report the store refused as
// too many: the seconds the store's error names, or else fullStoreRetry.
func storeRetryAfter(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		if seconds, convErr := strconv.Atoi(typed.Details["retry_after_seconds"]); convErr == nil && seconds > 0 {
			return strconv.Itoa(seconds)
		}
	}
	return retryAfter(fullStoreRetry)
}

func retryAfter(d time.Duration) string {
	seconds := int64((d + time.Second - 1) / time.Second)
	return strconv.FormatInt(max(seconds, 1), 10)
}

func orDefault[T int | int64 | time.Duration](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}
