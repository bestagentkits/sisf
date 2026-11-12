package dockerfilter_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Paths are judged as they were sent: encoded, escaped, unclean or
// absolute-form request targets are refused, not normalized.
func TestEncodedAndTraversingPathsAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, runner := e.create(t, "sisf-runner-x", validCreate())
	before := len(e.daemon.Requests())
	targets := []string{
		"POST " + version + "/containers/../images/create?fromImage=alpine",
		"POST " + version + "/containers/" + runner + "/../../images/create",
		"POST " + version + "/containers/" + runner + "%2F..%2F..%2Fimages%2Fcreate/start",
		"POST " + version + "/containers/" + runner + "%2fexec",
		"GET " + version + "/containers/%61cme-app-control/json",
		"GET " + version + "/containers/" + runner + "/json%3Fx",
		"GET /v1%2E45/_ping",
		"GET %2Fv1.45/_ping",
		"POST " + version + "//containers/create?name=sisf-runner-y",
		"POST " + version + "/containers/./create?name=sisf-runner-y",
		"GET " + version + "/containers/" + runner + "/json/",
		"GET " + version + "\\containers\\" + runner + "\\json",
		"GET " + version + "/containers/" + runner + "/json#frag",
		"GET http://docker" + version + "/containers/" + runner + "/json",
		"GET " + version + "/containers/" + runner + "/json?size=1",
		"GET " + version + "/containers/" + runner + "/logs?details=1",
		"GET " + version + "/containers/" + runner + "/logs?stdout=1&stdout=0",
		"POST " + version + "/containers/" + runner + "/wait?condition=anything",
		"POST " + version + "/containers/" + runner + "/stop?t=99999",
		"POST " + version + "/containers/" + runner + "/stop?signal=SIGKILL",
	}
	for _, target := range targets {
		status, body := e.raw(t, target+" HTTP/1.1\r\nHost: docker\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		if status != http.StatusBadRequest && status != http.StatusForbidden {
			t.Errorf("%s: status %d (%s)", target, status, strings.TrimSpace(body))
		}
	}
	for _, r := range e.daemon.Requests()[before:] {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.Path, "/json") {
			t.Fatalf("a refused request reached the daemon: %s %s?%s", r.Method, r.Path, r.Query)
		}
	}
}

func TestBodiesMustBeFramedAndSmall(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, runner := e.create(t, "sisf-runner-x", validCreate())
	good := encode(t, validCreate())
	createLine := "POST " + version + "/containers/create?name=sisf-runner-y HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\nConnection: close\r\n"

	chunked := createLine + "Transfer-Encoding: chunked\r\n\r\n" + fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(good), good)
	status, body := e.raw(t, chunked)
	mustStatus(t, status, http.StatusLengthRequired, "chunked create", body)

	big := `{"Cmd":["` + strings.Repeat("a", 70<<10) + `"]}`
	status, body = e.raw(t, createLine+fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(big), big))
	mustStatus(t, status, http.StatusRequestEntityTooLarge, "oversized create", body)

	status, body = e.raw(t, strings.Replace(createLine, "application/json", "text/plain", 1)+fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(good), good))
	mustStatus(t, status, http.StatusBadRequest, "create that is not JSON", body)

	startLine := "POST " + version + "/containers/" + runner + "/start HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n"
	status, body = e.raw(t, startLine+"Content-Length: 2\r\n\r\n{}")
	mustStatus(t, status, http.StatusBadRequest, "start with a body", body)
	status, body = e.raw(t, startLine+"Transfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\n\r\n")
	mustStatus(t, status, http.StatusBadRequest, "start with a chunked body", body)

	logs := "GET " + version + "/containers/" + runner + "/logs?stdout=1 HTTP/1.1\r\nHost: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n"
	status, body = e.raw(t, logs)
	mustStatus(t, status, http.StatusForbidden, "upgrade", body)

	if created := e.daemon.Created(); len(created) != 1 {
		t.Fatalf("%d creates reached the daemon, want only the first", len(created))
	}
	if e.reached(http.MethodPost, "/start") || e.reached(http.MethodGet, "/logs") {
		t.Fatal("a refused request reached the daemon")
	}
}
