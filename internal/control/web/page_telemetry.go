package web

import (
	"net/http"
	"strings"
)

// actionTelemetry searches traces, logs, activities and sessions. The factory
// does not offer it yet, so the page says so instead of showing anything.
const actionTelemetry = "telemetry.search"

type telemetryView struct {
	Frame
	Release, Issue, Run, Feature, Query string
	Result                              Panel
	Table                               Table
}

func (h *handler) telemetry(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	view := telemetryView{Frame: h.frame(s, "telemetry", "/telemetry"), Release: strings.TrimSpace(q.Get("release")),
		Issue: strings.TrimSpace(q.Get("issue")), Run: strings.TrimSpace(q.Get("run")),
		Feature: strings.TrimSpace(q.Get("feature")), Query: strings.TrimSpace(q.Get("q"))}
	input := map[string]string{}
	for key, value := range map[string]string{"release": view.Release, "issue": view.Issue, "run": view.Run, "feature": view.Feature, "query": view.Query} {
		if value != "" {
			input[key] = value
		}
	}
	view.Result = h.read(r, s, actionTelemetry, input)
	if view.Result.OK() {
		view.Table = tableFrom(view.Result.Output)
	}
	h.render(w, r, statusOfPanels(view.Result), pageTelemetry, view)
}
