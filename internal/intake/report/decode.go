package report

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// submission is the only shape a report may have. Unknown fields are
// refused, so no request can carry anything that looks like a setting.
type submission struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// decode reads and checks one submission. On failure it returns the HTTP
// status and a fixed message that repeats nothing of the request.
func decode(w http.ResponseWriter, r *http.Request, maxBytes int64) (submission, int, string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return submission{}, http.StatusUnsupportedMediaType, "a report is sent as application/json"
	}
	if r.ContentLength > maxBytes {
		return submission{}, http.StatusRequestEntityTooLarge, "the report is too large"
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	var in submission
	if err := dec.Decode(&in); err != nil {
		return submission{}, statusOf(err), "a report is one JSON object with a title and a body"
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return submission{}, statusOf(err), "a report is one JSON object with a title and a body"
	}
	if !validText(in.Title, false) || utf8.RuneCountInString(in.Title) > MaxTitleRunes || !validText(in.Body, true) {
		return submission{}, http.StatusBadRequest, "a report needs a title of at most 200 characters and a body, as printable text"
	}
	return in, 0, ""
}

func statusOf(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// validText requires non-blank, valid UTF-8 without control characters; a
// body may also hold line breaks and tabs.
func validText(s string, multiline bool) bool {
	if strings.TrimSpace(s) == "" || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return unicode.IsControl(r)
	})
}
