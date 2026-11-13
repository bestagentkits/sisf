package managedapp

// Check is one behaviour of the application the Owner authorised, as a short
// HTTP journey run against a revision. Every step must answer as expected
// within MaxLatencyMS for the check to pass.
type Check struct {
	Name string `yaml:"name" json:"name"`
	// MaxLatencyMS bounds each step; 0 means 5000.
	MaxLatencyMS int    `yaml:"max_latency_ms,omitempty" json:"max_latency_ms,omitempty"`
	Steps        []Step `yaml:"steps" json:"steps"`
	// Writes says the journey changes data (a purchase). Such a check runs
	// against a disposable copy of the data, never the live one.
	Writes bool `yaml:"writes,omitempty" json:"writes,omitempty"`
}

// Step is one request of a Check. Path, header values and Body may name
// values captured by earlier steps of the same check as ${name}.
type Step struct {
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	Path    string            `yaml:"path" json:"path"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Body is sent as JSON when set.
	Body string `yaml:"body,omitempty" json:"body,omitempty"`
	// Status is the expected status; 0 means 200.
	Status int `yaml:"status,omitempty" json:"status,omitempty"`
	// Contains is text the body must contain.
	Contains string `yaml:"contains,omitempty" json:"contains,omitempty"`
	// JSON maps a dotted path in the JSON body (a number indexes an array)
	// to the value it must have.
	JSON map[string]string `yaml:"json,omitempty" json:"json,omitempty"`
	// CaptureHeader and CaptureJSON keep a response header or JSON value
	// under a name for later steps.
	CaptureHeader map[string]string `yaml:"capture_header,omitempty" json:"capture_header,omitempty"`
	CaptureJSON   map[string]string `yaml:"capture_json,omitempty" json:"capture_json,omitempty"`
}

// DefaultMaxLatencyMS bounds a step of a check that names no bound.
const DefaultMaxLatencyMS = 5000

// Latency returns the bound of each step of c, in milliseconds.
func (c Check) Latency() int {
	if c.MaxLatencyMS > 0 {
		return c.MaxLatencyMS
	}
	return DefaultMaxLatencyMS
}
