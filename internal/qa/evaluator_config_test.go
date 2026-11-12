package qa_test

import (
	"testing"
	"time"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestAnEvaluatorIsNotBuiltWithAMissingPart(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*evaluator.Config){
		"no factory":            func(c *evaluator.Config) { c.FactoryID = "" },
		"no identity":           func(c *evaluator.Config) { c.Identity = " " },
		"no workspace":          func(c *evaluator.Config) { c.Workspace = "" },
		"no judge":              func(c *evaluator.Config) { c.Judge = nil },
		"no baseline":           func(c *evaluator.Config) { c.Baselines = nil },
		"no build ledger":       func(c *evaluator.Config) { c.Builds = nil },
		"no review criteria":    func(c *evaluator.Config) { c.Review = nil },
		"no signer":             func(c *evaluator.Config) { c.Signer = nil },
		"no verifier":           func(c *evaluator.Config) { c.Verifier = nil },
		"no clock":              func(c *evaluator.Config) { c.Clock = nil },
		"negative lifetime":     func(c *evaluator.Config) { c.ReceiptTTL = -time.Second },
		"lifetime too long":     func(c *evaluator.Config) { c.ReceiptTTL = evaluator.MaxReceiptTTL + time.Second },
		"unknown route action":  func(c *evaluator.Config) { c.SameRouteAction = "ask_the_model" },
		"route without a model": func(c *evaluator.Config) { c.Routes = []core.ModelRoute{{Provider: "openai"}} },
		"a gate missing":        func(c *evaluator.Config) { c.Gates = c.Gates[1:] },
		"a gate twice":          func(c *evaluator.Config) { c.Gates = append(c.Gates, c.Gates[0]) },
		"a nil gate":            func(c *evaluator.Config) { c.Gates = append(c.Gates, nil) },
		"an unknown gate":       func(c *evaluator.Config) { c.Gates = append(c.Gates, gate{name: "vibes"}) },
		"independence supplied": func(c *evaluator.Config) {
			c.Gates = append(c.Gates, gate{name: qa.GateIndependentEvaluator, passed: true})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			mutate(&e.cfg)
			if _, err := evaluator.New(e.cfg); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Fatalf("want %s, got %v", core.CodeInvalidArgument, err)
			}
		})
	}
}
