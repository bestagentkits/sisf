package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// maxHeadBytes bounds a request head.
const maxHeadBytes = 32 << 10

// errHeadTooLarge is a request head beyond maxHeadBytes.
var errHeadTooLarge = errors.New("request head too large")

// request is a request read from a client, with the Host header values as
// the client sent them: the standard parser drops them, and a Host header
// that disagrees with the request target is how a request is smuggled to
// another host than the one checked.
type request struct {
	*http.Request
	hosts  []string
	reader *bufio.Reader
}

// handle serves one client connection: one request, then the connection
// closes, or one tunnel.
func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(p.cfg.HeaderTimeout))
	req, err := readRequest(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errHeadTooLarge) {
			status = http.StatusRequestHeaderFieldsTooLarge
		}
		p.block(ctx, c, status, blockedAt(c, "", "", ""), "the request could not be read")
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if req.Method == http.MethodConnect {
		p.connect(ctx, c, req)
		return
	}
	p.forward(ctx, c, req)
}

// readRequest reads the head byte for byte, keeps its Host headers and
// then parses the whole request.
func readRequest(c net.Conn) (request, error) {
	br := bufio.NewReaderSize(c, 4096)
	var head bytes.Buffer
	for {
		line, err := br.ReadSlice('\n')
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return request{}, err
		}
		head.Write(line)
		if head.Len() > maxHeadBytes {
			return request{}, errHeadTooLarge
		}
		if err == nil && (string(line) == "\r\n" || string(line) == "\n") && head.Len() > len(line) {
			break
		}
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(head.Bytes())))
	if _, err := tp.ReadLine(); err != nil {
		return request{}, err
	}
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return request{}, err
	}
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader(head.Bytes()), br))
	parsed, err := http.ReadRequest(reader)
	if err != nil {
		return request{}, err
	}
	return request{Request: parsed, hosts: header.Values("Host"), reader: reader}, nil
}

// connect opens a tunnel to a checked destination.
func (p *Proxy) connect(ctx context.Context, c net.Conn, req request) {
	host, port, err := net.SplitHostPort(req.RequestURI)
	b := blockedAt(c, req.Method, host, port)
	if err != nil {
		p.block(ctx, c, http.StatusBadRequest, b, "the CONNECT target must be host:port")
		return
	}
	portNum, ok := parsePort(port)
	if !ok || !portAllowed(p.cfg.ConnectPorts, portNum) {
		p.block(ctx, c, http.StatusForbidden, b, "port "+port+" is not allowed for CONNECT")
		return
	}
	if reason := hostHeaderMismatch(req.hosts, host, port, true); reason != "" {
		p.block(ctx, c, http.StatusBadRequest, b, reason)
		return
	}
	addrs, status, reason := p.decide(ctx, host)
	if reason != "" {
		p.block(ctx, c, status, b, reason)
		return
	}
	upstream, err := p.dialAny(ctx, addrs, portNum)
	if err != nil {
		p.block(ctx, c, http.StatusBadGateway, b, "the destination could not be reached")
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, req.reader) // the client's bytes, including any it sent early
		closeWrite(upstream)
		close(done)
	}()
	_, _ = io.Copy(c, upstream)
	closeWrite(c)
	<-done
}

// forward sends one plain-HTTP request to a checked destination. It never
// follows a redirect: the client gets the redirect, and the request it
// makes next is checked like any other.
func (p *Proxy) forward(ctx context.Context, c net.Conn, req request) {
	u := req.URL
	b := blockedAt(c, req.Method, u.Hostname(), u.Port())
	switch {
	case u.Scheme == "https":
		p.block(ctx, c, http.StatusBadRequest, b, "use CONNECT for https")
		return
	case u.Scheme != "http" || u.Host == "" || u.Opaque != "":
		p.block(ctx, c, http.StatusBadRequest, b, "only CONNECT and absolute http URLs are proxied")
		return
	case u.User != nil:
		p.block(ctx, c, http.StatusForbidden, b, "credentials in the URL are refused")
		return
	case req.Header.Get("Upgrade") != "":
		p.block(ctx, c, http.StatusForbidden, b, "protocol upgrades are refused")
		return
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	portNum, ok := parsePort(port)
	if !ok || !portAllowed(p.cfg.HTTPPorts, portNum) {
		p.block(ctx, c, http.StatusForbidden, b, "port "+port+" is not allowed for http")
		return
	}
	if reason := hostHeaderMismatch(req.hosts, u.Hostname(), port, false); reason != "" {
		p.block(ctx, c, http.StatusBadRequest, b, reason)
		return
	}
	addrs, status, reason := p.decide(ctx, u.Hostname())
	if reason != "" {
		p.block(ctx, c, status, b, reason)
		return
	}
	out := req.Clone(ctx)
	out.RequestURI, out.Host = "", u.Host
	stripHopByHop(out.Header)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return p.dialAny(ctx, addrs, portNum) },
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(out)
	if err != nil {
		p.block(ctx, c, http.StatusBadGateway, b, "the destination could not be reached")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	stripHopByHop(resp.Header)
	resp.Close = true
	_ = resp.Write(c) // the client went away; nothing is left to tell it
}

// hostHeaderMismatch returns why the Host headers disagree with the
// request target, or "" when they agree or are absent.
func hostHeaderMismatch(hosts []string, host, port string, connect bool) string {
	switch {
	case len(hosts) > 1:
		return "more than one Host header"
	case len(hosts) == 0:
		return ""
	}
	h, p := hosts[0], ""
	if sh, sp, err := net.SplitHostPort(h); err == nil {
		h, p = sh, sp
	} else if strings.Contains(strings.Trim(h, "[]"), ":") && !strings.HasPrefix(h, "[") {
		return "the Host header is malformed"
	}
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	target := strings.TrimSuffix(strings.ToLower(host), ".")
	if p == "" && !connect {
		p = "80"
	}
	if h != target || (p != "" && p != port) {
		return "the Host header names another destination than the request target"
	}
	return ""
}

// decide applies the policy and the address checks to a destination host.
// It returns the addresses to connect to, or a status and the reason the
// destination is blocked.
func (p *Proxy) decide(ctx context.Context, host string) ([]netip.Addr, int, string) {
	policy, err := p.cfg.Policy.EgressPolicy(ctx)
	if err == nil {
		err = policy.Validate()
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable, "the egress policy is unavailable"
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		switch {
		case policy.Mode != core.EgressUnrestricted:
			return nil, http.StatusForbidden, "IP address literals are not allowed; name the host"
		case !publicAddress(addr):
			return nil, http.StatusForbidden, "the address is private, loopback, link-local or metadata"
		}
		return []netip.Addr{addr.Unmap()}, 0, ""
	}
	name, ok := normalizeHost(host)
	switch {
	case !ok:
		return nil, http.StatusForbidden, "the destination is not a valid host name"
	case looksNumeric(name):
		return nil, http.StatusForbidden, "the host name is an IP address in another notation"
	case !Allows(policy, name):
		return nil, http.StatusForbidden, "the host is not on the egress allowlist"
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := p.cfg.Resolver.LookupNetIP(lookup, "ip", name)
	if err != nil || len(addrs) == 0 {
		return nil, http.StatusBadGateway, "the host does not resolve"
	}
	for i, a := range addrs {
		if !publicAddress(a) {
			return nil, http.StatusForbidden, "the host resolves to a private, loopback, link-local or metadata address"
		}
		addrs[i] = a.Unmap()
	}
	return addrs, 0, ""
}

// dialAny connects to the first checked address that answers.
func (p *Proxy) dialAny(ctx context.Context, addrs []netip.Addr, port uint16) (net.Conn, error) {
	var last error
	for _, a := range addrs {
		conn, err := p.cfg.Dial(ctx, "tcp", netip.AddrPortFrom(a, port).String())
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, fmt.Errorf("no address answered: %w", last)
}

// block records a refused attempt and tells the client why.
func (p *Proxy) block(ctx context.Context, c net.Conn, status int, b Blocked, reason string) {
	b.At, b.Reason = p.cfg.Clock.Now(), reason
	p.cfg.Recorder.RecordBlocked(ctx, b)
	p.cfg.Logger.Warn("egress blocked", "client", b.Client, "method", b.Method, "host", b.Host, "port", b.Port, "reason", reason)
	body := "blocked by the sisf egress policy: " + reason + "\n"
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nX-Sisf-Egress: blocked\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

func blockedAt(c net.Conn, method, host, port string) Blocked {
	return Blocked{Client: c.RemoteAddr().String(), Method: method, Host: truncate(host, 255), Port: truncate(port, 8)}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func parsePort(port string) (uint16, bool) {
	n, err := strconv.ParseUint(port, 10, 16)
	return uint16(n), err == nil && n > 0 && port == strconv.FormatUint(n, 10)
}

// hopByHop are headers that concern one connection only, and the proxy's
// own credentials, which never travel on.
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Authenticate",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHopByHop(h http.Header) {
	for _, field := range h.Values("Connection") {
		for _, name := range strings.Split(field, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
