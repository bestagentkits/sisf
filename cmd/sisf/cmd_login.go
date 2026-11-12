package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Where the CLI reads the control URL and a token from, in order of
// precedence: the flag, then the environment, then the stored login.
const (
	controlURLEnv     = "SISF_URL"
	tokenEnv          = "SISF_TOKEN"
	defaultControlURL = "http://127.0.0.1:7463"
	credentialsFile   = "credentials.json"
)

// Paths of the REST surface the CLI talks to; they mirror the control
// server's API.
const (
	deviceStartPath = "/api/v1/device/start"
	deviceTokenPath = "/api/v1/device/token"
	actionPath      = "/api/v1/actions/"
)

// loginClock drives the polling waits; tests replace it.
var loginClock core.Clock = core.SystemClock{}

// credentials is the stored login. The token is a secret: the file is
// owner-only and the value is never printed.
type credentials struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// credentialsPath is where the login of the install in dir is stored.
func credentialsPath(dir string) (string, error) {
	home, err := resolveDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, credentialsFile), nil
}

// saveCredentials writes the login owner-only through a temporary file, so a
// reader never sees a partial token and the token is never briefly readable
// by others.
func saveCredentials(path string, c credentials) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return core.NewError(core.CodeInternal, "the login cannot be encoded").WithCause(err)
	}
	fail := func(err error) error {
		return core.NewError(core.CodeUnavailable, "the login could not be stored under the SISF home").WithCause(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fail(err)
	}
	return nil
}

// loadCredentials reads the stored login; ok is false when there is none.
func loadCredentials(path string) (c credentials, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, core.NewError(core.CodeUnavailable, "the stored login could not be read").WithCause(err)
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.URL == "" || c.Token == "" {
		return credentials{}, false, core.NewError(core.CodeInvalidArgument, "the stored login is damaged; run sisf login again")
	}
	return c, true, nil
}

// connFlags select the factory the CLI talks to.
type connFlags struct {
	url, dir string
	insecure bool
}

func (c *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "url", "", "control URL of the factory (default $"+controlURLEnv+", the stored login, or "+defaultControlURL+")")
	fs.StringVar(&c.dir, "dir", "", "SISF home holding the stored login (default $"+homeEnv+" or ~/.sisf/factory)")
	fs.BoolVar(&c.insecure, "allow-insecure-http", false, "send credentials over plain http to a non-loopback host")
}

// client builds the HTTP client for the chosen factory. With needToken it
// also finds the token: $SISF_TOKEN, else the stored login, which is used
// only for the URL it was issued for and only until it expires.
func (c *connFlags) client(needToken bool) (*apiClient, string, error) {
	path, err := credentialsPath(c.dir)
	if err != nil {
		return nil, "", err
	}
	stored, haveStored, err := loadCredentials(path)
	if err != nil && needToken {
		return nil, "", err
	}
	base, err := normalizeControlURL(firstNonEmpty(c.url, os.Getenv(controlURLEnv), stored.URL, defaultControlURL), c.insecure)
	if err != nil {
		return nil, "", err
	}
	client := &apiClient{base: base, hc: &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if !needToken {
		return client, path, nil
	}
	switch {
	case os.Getenv(tokenEnv) != "":
		client.token = os.Getenv(tokenEnv)
	case haveStored && stored.URL == base && loginClock.Now().Before(stored.ExpiresAt):
		client.token = stored.Token
	case haveStored && stored.URL == base:
		return nil, "", core.NewError(core.CodeUnauthenticated, "the stored login expired; run sisf login")
	default:
		return nil, "", core.NewError(core.CodeUnauthenticated, "no login for "+base+"; run sisf login or set "+tokenEnv)
	}
	return client, path, nil
}

// normalizeControlURL accepts an http(s) base URL. Credentials travel over
// plain http only to a loopback host unless the caller insists.
func normalizeControlURL(raw string, insecure bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", usagef("the control URL must look like https://host:port")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); u.Scheme == "http" && !insecure && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", usagef("refusing to send credentials over plain http to %s; use https or --allow-insecure-http", host)
	}
	return u.String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	register(Command{
		Path:    []string{"login"},
		Args:    "[--url url] [--dir path] [--name label] [--scope scope]...",
		Summary: "Sign the CLI in through the browser and store a short-lived token",
		Help: `
Starts a login request on the running factory and prints a verification URL
and a code. Open the URL in a browser, sign in, check that the code matches,
and approve. The CLI then receives a short-lived token limited to the scopes
asked for (default: every scope) and stores it owner-only (0600) as
` + credentialsFile + ` under the SISF home. The token is never printed.

The control URL comes from --url, then $` + controlURLEnv + `, then the default ` + defaultControlURL + `.
Approving needs a recent identity confirmation; the page offers it.`,
		Flags: bindLoginFlags(&connFlags{}, new(string), &stringList{}),
		Run:   runLogin,
	})
}

// bindLoginFlags registers login's flags: the factory to sign in to, the
// label of the token and the scopes it may carry.
func bindLoginFlags(conn *connFlags, name *string, scopes *stringList) func(*flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		conn.register(fs)
		fs.StringVar(name, "name", "sisf-cli", "label shown on the approval page")
		fs.Var(scopes, "scope", "scope the token may carry (repeatable; default: all)")
	}
}

func runLogin(ctx context.Context, env *Env, args []string) error {
	fs := newFlagSet("login")
	var conn connFlags
	name := new(string)
	var scopes stringList
	bindLoginFlags(&conn, name, &scopes)(fs)
	if _, err := parseInterspersed(fs, args, 0, ""); err != nil {
		return err
	}
	client, path, err := conn.client(false)
	if err != nil {
		return err
	}
	if err := conn.verifyFactory(ctx, client); err != nil {
		return err
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		Complete   string `json:"verification_uri_complete"`
		Interval   int    `json:"interval"`
	}
	if err := client.postJSON(ctx, deviceStartPath, map[string]any{"name": *name, "scopes": []string(scopes)}, &start); err != nil {
		return err
	}
	if err := env.Printf("Open %s\nand confirm the code %s\nWaiting for approval in the browser...\n", start.Complete, start.UserCode); err != nil {
		return err
	}
	token, err := pollForToken(ctx, client, start.DeviceCode, time.Duration(start.Interval)*time.Second)
	if err != nil {
		return err
	}
	if err := saveCredentials(path, credentials{URL: client.base, Token: token.Token, ExpiresAt: token.ExpiresAt}); err != nil {
		return err
	}
	return env.Printf("Signed in to %s. The token expires at %s and is stored in %s.\n", client.base, token.ExpiresAt.Format(time.RFC3339), path)
}

type issuedLogin struct {
	Status    string    `json:"status"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// pollForToken asks until the person approved, the request expired or ctx
// ends. A server that says "too fast" is obeyed.
func pollForToken(ctx context.Context, c *apiClient, deviceCode string, interval time.Duration) (issuedLogin, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return issuedLogin{}, core.NewError(core.CodeInternal, "the poll cannot be encoded").WithCause(err)
	}
	for {
		select {
		case <-ctx.Done():
			return issuedLogin{}, core.NewError(core.CodeTimeout, "the login was interrupted before it was approved")
		case <-loginClock.After(interval):
		}
		status, raw, header, err := c.do(ctx, http.MethodPost, deviceTokenPath, bytes.NewReader(body))
		switch {
		case err != nil:
			return issuedLogin{}, err
		case status == http.StatusAccepted:
		case status == http.StatusTooManyRequests:
			if secs, perr := strconv.Atoi(header.Get("Retry-After")); perr == nil && secs > 0 {
				interval = max(interval, time.Duration(secs)*time.Second)
			}
		case status == http.StatusOK:
			var out issuedLogin
			if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
				return issuedLogin{}, core.NewError(core.CodeUnavailable, "the server answered the login without a token")
			}
			return out, nil
		default:
			return issuedLogin{}, failureOf(status, raw)
		}
	}
}
