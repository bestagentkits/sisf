package fakegithub

import (
	"net/http"
	"strings"
)

// routeFault answers the next request of one method whose path contains a
// fragment, instead of handling it.
type routeFault struct {
	method, fragment string
	status           int
	message          string
}

// FailOn answers the next request of method whose path contains fragment
// (for example "/git/trees" or "/contents/") with status and message, as
// GitHub would refuse it. Faults are used in the order they were added.
func (g *GitData) FailOn(method, fragment string, status int, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routeFaults = append(g.routeFaults, routeFault{method: method, fragment: fragment, status: status, message: message})
}

// takeRouteFault removes and returns the first fault r matches.
func (g *GitData) takeRouteFault(r *http.Request) (routeFault, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, f := range g.routeFaults {
		if f.method == r.Method && strings.Contains(r.URL.Path, f.fragment) {
			g.routeFaults = append(g.routeFaults[:i], g.routeFaults[i+1:]...)
			return f, true
		}
	}
	return routeFault{}, false
}
