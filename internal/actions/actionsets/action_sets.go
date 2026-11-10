// Package actionsets is the one place that knows every action set of the
// factory. The control process registers the real sets with Register; the
// generator of api/openapi.yaml and api/llms.txt describes the same sets
// with Describe, so the published documents and the running registry
// cannot drift apart.
//
// Both start from the stand-ins of package pendingactions and then register
// every real set over them, so a stand-in can only remain when a real set is
// missing, which the tests of this package refuse.
package actionsets

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/internal/actions/operationsactions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/actions/setupactions"
	"github.com/bestagentkits/sisf/internal/actions/telemetryactions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Deps is what the real action sets are built from. Every field is required.
type Deps struct {
	FactoryID core.FactoryID
	Runs      runactions.Deps
	Approvals approvalactions.Inbox
	Budget    budget.ActionsConfig
	// SecretProviders list the secret references; values are never read.
	SecretProviders []secrets.SecretProvider
	Catalog         catalog.ActionsConfig
	Telemetry       telemetry.Reader
	// Repo is the factory's repository, where the sample issue is opened.
	Repo       github.RepoRef
	Issues     setupactions.IssueOpener
	Supervisor operationsactions.Supervisor
}

// Register adds the stand-ins, then every real set over them.
func Register(reg *actions.Registry, d Deps) error {
	if reg == nil {
		return core.NewError(core.CodeInvalidArgument, "the action sets need a registry")
	}
	if err := reg.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		return err
	}
	runs, err := runactions.New(d.Runs)
	if err != nil {
		return err
	}
	inbox, err := approvalactions.New(d.Approvals)
	if err != nil {
		return err
	}
	money, err := budget.Actions(d.Budget)
	if err != nil {
		return err
	}
	products, err := catalog.Actions(d.Catalog)
	if err != nil {
		return err
	}
	search, err := telemetryactions.Actions(d.Telemetry)
	if err != nil {
		return err
	}
	sample, err := setupactions.Actions(d.Repo, d.Issues)
	if err != nil {
		return err
	}
	for _, set := range [][]core.Action{runs, inbox, money, secrets.Actions(d.SecretProviders...), products, search, sample} {
		if err := reg.Register(set...); err != nil {
			return err
		}
	}
	return operationsactions.Register(reg, operationsactions.Deps{FactoryID: d.FactoryID, Supervisor: d.Supervisor})
}

// Specs returns the spec of every action the real sets serve.
func Specs() ([]core.ActionSpec, error) {
	specs := append(append(runactions.Specs(), approvalactions.Specs()...), operationsactions.Specs()...)
	specs = append(specs, telemetryactions.Spec(), setupactions.Spec())
	money, err := budget.Actions(budget.ActionsConfig{Store: describedStore{}})
	if err != nil {
		return nil, err
	}
	products, err := catalog.Actions(catalog.ActionsConfig{Publisher: &catalog.Publisher{}, Docs: describedDocs{}})
	if err != nil {
		return nil, err
	}
	for _, set := range [][]core.Action{money, secrets.Actions(), products} {
		for _, a := range set {
			specs = append(specs, a.Spec)
		}
	}
	return specs, nil
}

// Describe returns a registry of every action, registered the way Register
// does, whose handlers only refuse. It describes; it runs nothing.
func Describe() (*actions.Registry, error) {
	reg := actions.NewRegistry()
	if err := reg.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		return nil, err
	}
	specs, err := Specs()
	if err != nil {
		return nil, err
	}
	described := make([]core.Action, 0, len(specs))
	for _, spec := range specs {
		described = append(described, actions.NotSupported(spec, "this registry describes actions and runs none"))
	}
	return reg, reg.Register(described...)
}
