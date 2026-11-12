package systemone

import (
	"regexp"

	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// HeaderVersion carries the pinned protocol version on the request and the
// reply. A reply under another version is refused.
const HeaderVersion = "X-System-One-Version"

// MaxReplyBytes bounds a reply body; a longer reply is refused unread.
const MaxReplyBytes = 64 << 10

// Bounds on the points of doubt a judgment may name.
const (
	maxUncertainties = 16
)

// uncertaintyPattern accepts a named point of doubt such as
// "baseline_too_short", never free text.
var uncertaintyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// WireRequest is the body the adapter posts.
type WireRequest struct {
	Version string              `json:"version"`
	Backend Backend             `json:"backend"`
	Request pkgdecision.Request `json:"request"`
}

// WireReply is the only body the adapter accepts back. Any other field is
// refused, so an extension the adapter does not know cannot slip through.
type WireReply struct {
	Version  string               `json:"version"`
	Judgment pkgdecision.Judgment `json:"judgment"`
}
