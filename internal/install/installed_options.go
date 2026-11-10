package install

import (
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// installedOptions rebuilds, from an install's manifest and its budget
// confirmation, the options its units are started with: the profile, the
// sign-in settings, the agents and the budget. Secrets stay in the files.
func (in *Installer) installedOptions(m *manifest.Manifest) (Options, error) {
	o := Options{
		Dir: in.Layout.Dir, Mode: m.Mode, FactoryID: m.FactoryID, Repo: m.Repository,
		Sim: m.Extensions["profile"] == profileSim, Dev: devInstall(m), Debug: in.Debug,
		Identity: identityOf(m),
	}
	o.GitHubAPIURL, _ = m.Extensions["github_api_url"].(string)
	o.lifecycleExtensions(m)
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	if err != nil {
		return o, err
	}
	if ok {
		o.Budget = rec.choice()
	}
	return o, nil
}
