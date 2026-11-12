package control

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The client may be gone; there is nobody to tell about a failed write.
	_, _ = w.Write(append(body, '\n'))
}
