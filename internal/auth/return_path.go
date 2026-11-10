package auth

import (
	"net/url"
	"strings"
)

// LocalReturnPath keeps a return target on this site and turns anything
// else into "/". It is the one check every sign-in, step-up and dashboard
// redirect uses for a caller-supplied target.
//
// A kept target parses as a URL with no scheme, host or user, and its path
// starts with a single "/". The raw target and its decoded path hold no
// ASCII control character and no backslash, since browsers drop tabs and
// newlines and read a backslash as a slash, which can turn "/\t/evil.com"
// or "/\evil.com" into a protocol-relative URL. The decoded path holds no
// "//", so an encoded or dot-segment variant cannot become one either.
func LocalReturnPath(target string) string {
	const home = "/"
	if !strings.HasPrefix(target, "/") || hasUnsafeByte(target) {
		return home
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return home
	}
	if !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") || hasUnsafeByte(u.Path) {
		return home
	}
	return target
}

// hasUnsafeByte reports whether s holds an ASCII control character or a
// backslash.
func hasUnsafeByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}
