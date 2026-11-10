package systemone_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/decision/systemone/systemonetest"
)

// reply is a JSON body under the pinned version header.
func reply(body string) systemonetest.Reply {
	return systemonetest.Reply{Status: http.StatusOK, ContentType: "application/json", Version: version, Body: []byte(body)}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
