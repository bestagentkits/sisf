package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestEveryErrorCodeHasAnHTTPStatus(t *testing.T) {
	t.Parallel()
	for _, code := range core.ErrorCodes() {
		status, ok := statusByCode[code]
		if !ok || status < 400 || status > 599 {
			t.Errorf("%s has status %d (mapped: %v)", code, status, ok)
		}
	}
	if len(statusByCode) != len(core.ErrorCodes()) {
		t.Errorf("the table holds codes that do not exist: %d vs %d", len(statusByCode), len(core.ErrorCodes()))
	}
}

func TestWriteErrorKeepsTheNextActionsAndDropsTheCause(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	err := core.NewError(core.CodeConflict, "stale", core.NextAction{Action: "runs.inspect"}).WithCause(errors.New("secret-cause"))
	writeError(rec, err)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"runs.inspect"`) || strings.Contains(rec.Body.String(), "secret-cause") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	writeError(rec, errors.New("plain failure with secret-cause"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret-cause") || !strings.Contains(rec.Body.String(), `"INTERNAL"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
