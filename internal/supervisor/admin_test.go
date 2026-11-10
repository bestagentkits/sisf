package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// serve runs the supervisor's admin server until the test ends and reports
// how Serve returned.
func serve(t *testing.T, sup *Supervisor) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Serve(ctx) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func TestAdminAddressThatIsNotLocalIsRefused(t *testing.T) {
	t.Parallel()
	notLocal := []string{
		"0.0.0.0:7464",           // every interface
		":7464",                  // every interface, implied
		"[::]:7464",              // every interface, IPv6
		"192.168.1.10:7464",      // a LAN address
		"10.0.0.5:7464",          // a container network address
		"example.com:7464",       // a name that resolves elsewhere
		"supervisor:7464",        // a compose service name
		"127.0.0.1",              // no port
		"127.0.0.1:port",         // not a port
		"127.0.0.1:70000",        // not a port
		"tcp://127.0.0.1:7464",   // a URL, not an address
		"unix:",                  // no socket path
		"unix:supervisor.sock",   // relative socket path
		"unix:../supervisor.sok", // relative socket path
	}
	for _, addr := range notLocal {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if _, err := parseAdminAddr(addr); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Fatalf("parseAdminAddr(%q) = %v, want INVALID_ARGUMENT", addr, err)
			}
			d := newDeployment(t)
			d.cfg.AdminAddr = addr
			sup, err := d.start()
			refused(t, sup, err, core.CodeInvalidArgument)
			if _, err := FetchStatus(context.Background(), addr); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("FetchStatus(%q) = %v, want INVALID_ARGUMENT", addr, err)
			}
		})
	}
}

func TestAdminAddressOnLoopbackOrAUnixSocketIsAccepted(t *testing.T) {
	t.Parallel()
	local := map[string]adminAddr{
		"127.0.0.1:7464":                 {"tcp", "127.0.0.1:7464"},
		"127.0.0.1:0":                    {"tcp", "127.0.0.1:0"},
		"[::1]:7464":                     {"tcp", "[::1]:7464"},
		"localhost:7464":                 {"tcp", "127.0.0.1:7464"},
		"unix:/run/sisf/supervisor.sock": {"unix", "/run/sisf/supervisor.sock"},
	}
	for addr, want := range local {
		got, err := parseAdminAddr(addr)
		if err != nil || got != want {
			t.Errorf("parseAdminAddr(%q) = %+v, %v; want %+v", addr, got, err, want)
		}
	}
	if got, err := parseAdminAddr(DefaultAdminAddr); err != nil || got.address != "127.0.0.1:7464" {
		t.Errorf("default admin address = %+v, %v; want loopback port 7464", got, err)
	}
}

func TestStatusIsServedOnALoopbackPort(t *testing.T) {
	t.Parallel()
	sup := newDeployment(t).mustStart()
	host, _, err := net.SplitHostPort(sup.AdminAddr())
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("admin address = %q (%v), want a loopback address", sup.AdminAddr(), err)
	}
	stop := serve(t, sup)

	status, err := FetchStatus(context.Background(), sup.AdminAddr())
	if err != nil {
		t.Fatalf("FetchStatus: %v", err)
	}
	if !reflect.DeepEqual(status, sup.Status()) {
		t.Errorf("status over the socket = %+v, want %+v", status, sup.Status())
	}
	if err := stop(); err != nil {
		t.Errorf("Serve returned %v after it was asked to stop", err)
	}
}

func TestStatusIsServedOnAUnixSocketOnlyItsOwnerCanOpen(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	socket := filepath.Join(shortTempDir(t), "admin.sock")
	d.cfg.AdminAddr = "unix:" + socket
	sup := d.mustStart()

	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("admin socket = %v, %v; want a socket", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("admin socket mode = %04o, want 0600", info.Mode().Perm())
	}
	stop := serve(t, sup)
	status, err := FetchStatus(context.Background(), sup.AdminAddr())
	if err != nil || status.FactoryID != testFactory {
		t.Fatalf("FetchStatus = %+v, %v", status, err)
	}
	if err := stop(); err != nil {
		t.Errorf("Serve returned %v after it was asked to stop", err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the admin socket was left behind after shutdown (stat: %v)", err)
	}
}

func TestAdminSocketOnlyAnswersReads(t *testing.T) {
	t.Parallel()
	sup := newDeployment(t).mustStart()
	handler := newAdminHandler(sup.Status)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/status", "/"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(`{"self_check":{"passed":false}}`)))
			if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want it refused", method, path, response.Code)
			}
		}
	}
	for _, path := range []string{"/", "/lkg", "/release", "/status/", "/debug/pprof/"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: status is the only thing served", path, response.Code)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET /status = %d %q", response.Code, response.Header().Get("Content-Type"))
	}
	var status Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.SelfCheck.Passed || status.SelfCheck.ExecutableDigest != runningDigest {
		t.Errorf("status = %+v, want the passed self check", status)
	}
	if !sup.Status().SelfCheck.Passed {
		t.Error("requests changed the supervisor's state")
	}
}

func TestAdminSocketDoesNotReplaceAFileThatIsNotASocket(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	path := filepath.Join(shortTempDir(t), "admin.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	d.cfg.AdminAddr = "unix:" + path

	sup, err := d.start()
	refused(t, sup, err, core.CodeAlreadyExists)
	if content, err := os.ReadFile(path); err != nil || string(content) != "keep me" {
		t.Errorf("the file at the socket path was changed: %q, %v", content, err)
	}
}

func TestSecondSupervisorDoesNotTakeOverALiveAdminSocket(t *testing.T) {
	t.Parallel()
	first := newDeployment(t)
	socket := filepath.Join(shortTempDir(t), "admin.sock")
	first.cfg.AdminAddr = "unix:" + socket
	running := first.mustStart()
	serve(t, running)

	second := newDeployment(t)
	second.cfg.AdminAddr = first.cfg.AdminAddr
	sup, err := second.start()
	refused(t, sup, err, core.CodeAlreadyExists)

	if _, err := FetchStatus(context.Background(), running.AdminAddr()); err != nil {
		t.Errorf("the running supervisor stopped answering: %v", err)
	}
}

func TestSocketLeftByAStoppedSupervisorIsReused(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	socket := filepath.Join(shortTempDir(t), "admin.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stale.SetUnlinkOnClose(false) // as after a crash: the file stays, nobody listens
	if err := stale.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	d.cfg.AdminAddr = "unix:" + socket

	sup := d.mustStart()
	serve(t, sup)
	if _, err := FetchStatus(context.Background(), sup.AdminAddr()); err != nil {
		t.Errorf("FetchStatus: %v", err)
	}
}

func TestStatusOfASupervisorThatIsNotRunningIsUnavailable(t *testing.T) {
	t.Parallel()
	absent := "unix:" + filepath.Join(shortTempDir(t), "admin.sock")
	if _, err := FetchStatus(context.Background(), absent); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("FetchStatus = %v, want UNAVAILABLE", err)
	}
}
