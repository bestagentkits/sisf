package supervisor

import (
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// unixScheme marks an admin address that is a Unix socket path.
const unixScheme = "unix:"

// staleProbeTimeout bounds the check for a live owner of an existing socket.
// A socket nobody listens on refuses at once, so the wait is rarely used.
const staleProbeTimeout = time.Second

// adminAddr is a validated admin address: a network and an address the net
// package understands. Only parseAdminAddr builds one, so every admin
// address in the program is local by construction.
type adminAddr struct {
	network string
	address string
}

// String renders the address in the form parseAdminAddr accepts.
func (a adminAddr) String() string {
	if a.network == "unix" {
		return unixScheme + a.address
	}
	return a.address
}

// parseAdminAddr accepts only an address that cannot be reached from another
// machine: "unix:/absolute/path", or "host:port" where host is a loopback IP
// address or the name localhost. An empty host, a wildcard, any other
// address and any other name are refused; a name is never resolved, because
// what it resolves to can change.
func parseAdminAddr(addr string) (adminAddr, error) {
	if path, isUnix := strings.CutPrefix(addr, unixScheme); isUnix {
		if !filepath.IsAbs(path) {
			return adminAddr{}, invalid("admin socket %q must be an absolute path after %q", addr, unixScheme)
		}
		return adminAddr{network: "unix", address: filepath.Clean(path)}, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return adminAddr{}, invalid("admin address %q must be unix:/path or a loopback host:port", addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return adminAddr{}, invalid("admin address %q has no valid port", addr)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Unmap().IsLoopback() {
		return adminAddr{}, invalid("admin address %q is not local; the supervisor listens only on loopback or a Unix socket", addr)
	}
	return adminAddr{network: "tcp", address: net.JoinHostPort(host, port)}, nil
}

// listenAdmin opens the admin listener. An address already in use is an
// error: the supervisor never moves to another port or takes over a socket
// that another process is serving.
func listenAdmin(addr adminAddr) (net.Listener, error) {
	if addr.network == "unix" {
		if err := clearStaleSocket(addr.address); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen(addr.network, addr.address)
	if err != nil {
		return nil, newError(core.CodeUnavailable, "the admin socket %s cannot be opened", addr).WithCause(err)
	}
	if addr.network == "unix" {
		// Only the account the supervisor runs as may connect.
		if err := os.Chmod(addr.address, 0o600); err != nil {
			_ = listener.Close() // the chmod failure is the one reported
			return nil, newError(core.CodeUnavailable, "the admin socket %s cannot be restricted to its owner", addr).WithCause(err)
		}
	}
	return listener, nil
}

// clearStaleSocket removes a socket file left behind by a supervisor that
// stopped without cleaning up. It leaves everything else alone: a file that
// is not a socket is never deleted, and a socket that answers belongs to a
// running process.
func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newError(core.CodeUnavailable, "the admin socket path %s cannot be inspected", path).WithCause(err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return newError(core.CodeAlreadyExists, "the admin socket path %s exists and is not a socket", path)
	}
	if conn, err := net.DialTimeout("unix", path, staleProbeTimeout); err == nil {
		_ = conn.Close() // the probe carried no data
		return newError(core.CodeAlreadyExists, "another process is already serving the admin socket %s", path)
	}
	if err := os.Remove(path); err != nil {
		return newError(core.CodeUnavailable, "the stale admin socket %s cannot be removed", path).WithCause(err)
	}
	return nil
}
