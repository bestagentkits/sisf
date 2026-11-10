package release

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestAPipelineRefusesParametersThatWeakenTheReleaseRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	cases := map[string]func(*Config){
		"no state store":                func(c *Config) { c.State = nil },
		"no emergency stop":             func(c *Config) { c.Freeze = nil },
		"no publisher":                  func(c *Config) { c.Publisher = nil },
		"canary that never reaches 100": func(c *Config) { c.CanarySteps = []int{5, 25} },
		"canary steps that go down":     func(c *Config) { c.CanarySteps = []int{25, 5, 100} },
		"threshold of 100 percent":      func(c *Config) { c.Monitor.ErrorRateThreshold = 1 },
		"lease shorter than a window":   func(c *Config) { c.LeaseTTL = time.Minute },
		"pass score above 100":          func(c *Config) { c.PassScore = 101 },
		"no protected paths":            func(c *Config) { c.Protected = []string{} },
	}
	for name, change := range cases {
		cfg := w.cfg
		change(&cfg)
		if _, err := New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: New = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
