package install

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// InitResult says what `init` made. It never carries a secret value.
type InitResult struct {
	FactoryID core.FactoryID
	Options   Options
	// Warnings are things the Owner must read.
	Warnings []string
}

// rootKeyWarning is shown at every init: the lower-assurance local profile.
const rootKeyWarning = "The root signing key is stored in %s. This is the lower-assurance local " +
	"profile: anyone who can read that directory can sign releases for this factory. " +
	"Keep the key outside the factory (KMS, HSM or a hardware key) before you rely on self-update."

// devWarning is shown at the init of a development install and by the doctor.
const devWarning = "DEVELOPMENT INSTALL: anyone who can reach 127.0.0.1 on this machine signs in as the Owner through the fake identity " +
	"provider on 127.0.0.1:7468, without a password. Use it on a machine only you use, never for a factory others rely on."

// Init creates a new install: identity, the sign-in settings, one
// credential set per zone, the root key, the manifest, and the service
// units. It refuses to
// touch a directory that already holds an install, so one directory is
// exactly one factory_id for its whole life.
func (in *Installer) Init(ctx context.Context, o Options) (InitResult, error) {
	o, err := o.normalize()
	if err != nil {
		return InitResult{}, err
	}
	l := in.Layout
	if l.Dir != o.Dir {
		return InitResult{}, invalidf("the options and the installer name different directories")
	}
	if done, err := l.installed(); err != nil {
		return InitResult{}, unavailablef(err, "cannot inspect %s", l.Dir)
	} else if done {
		return InitResult{}, core.NewError(core.CodeAlreadyExists, "an install already exists in "+l.Dir,
			core.NextAction{Action: "sisf up", Description: "start the existing install"})
	}
	if o.GitHubCredential == GitHubCredentialGHCLI {
		if o.GitHubToken, err = in.ghToken(ctx); err != nil {
			return InitResult{}, err
		}
	}
	if err := os.MkdirAll(l.Dir, secretDirMode); err != nil {
		return InitResult{}, unavailablef(err, "cannot create %s", l.Dir)
	}
	if err := in.writeSecrets(o); err != nil {
		return InitResult{}, err
	}
	m := buildManifest(o)
	if err := conformance.Check(m).Err(); err != nil {
		return InitResult{}, err
	}
	if rec, ok := o.Budget.confirmation(in.Clock.Now()); ok {
		if err := writeBudgetConfirmation(l.BudgetConfirmation(), rec); err != nil {
			return InitResult{}, err
		}
	}
	if o.Mode == manifest.ModeStandalone {
		if err := in.writeServiceUnits(o); err != nil {
			return InitResult{}, err
		}
	}
	// The manifest is written last: its presence is what makes a directory an install.
	if err := writeManifest(l.Config(), m); err != nil {
		return InitResult{}, err
	}
	res := InitResult{FactoryID: o.FactoryID, Options: o}
	res.Warnings = append(res.Warnings, fmt.Sprintf(rootKeyWarning, l.SecretsDir(areaControl)))
	if o.Dev {
		res.Warnings = append(res.Warnings, devWarning)
	}
	if !o.Budget.Confirmed {
		res.Warnings = append(res.Warnings, "The unlimited budget is not confirmed: the factory takes in issues and starts no work until you confirm it.")
	}
	return res, nil
}

// writeSecrets generates every secret of the install. Each is written for
// its owner only, and none is printed.
func (in *Installer) writeSecrets(o Options) error {
	l := in.Layout
	rnd := in.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	var failed error
	tok := func() string {
		t, err := newToken(rnd)
		if err != nil && failed == nil {
			failed = err
		}
		return t
	}
	pw := map[core.Zone]string{core.ZoneApplication: tok(), core.ZoneAgent: tok(), core.ZoneControl: tok()}
	webhook, statePW := tok(), tok()
	simToken, qa := tok(), tok()
	edgeAdmin, edgeCounters, dispatcherAdmin := tok(), tok(), tok()
	// The control zone signs approval decisions with an ed25519 key; a token
	// is 32 random bytes in unpadded base64url, which is the seed's format.
	approvalKey := tok()
	if failed != nil {
		return failed
	}
	pub, priv, err := ed25519.GenerateKey(rnd)
	if err != nil {
		return unavailablef(err, "cannot generate the root key")
	}
	pem, err := signing.EncodePrivateKey(priv)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		l.Secret(areaControl, "root-key.pem"):    pem,
		l.Secret(areaControl, "root-public-key"): []byte(signing.EncodePublicKey(pub) + "\n"),
		l.Secret(areaControl, "webhook-secret"):  []byte(webhook + "\n"),
		l.Secret(areaApplication, "credentials"): []byte(pw[core.ZoneApplication] + "\n"),
		l.Secret(areaAgent, "credentials"):       []byte(pw[core.ZoneAgent] + "\n"),
		l.Secret(areaControl, "credentials"):     []byte(pw[core.ZoneControl] + "\n"),
		l.Secret(areaAgent, "qa-credentials"):    []byte(qa + "\n"),
	}
	token := o.GitHubToken
	if token.IsZero() && o.Sim {
		// The fake accepts any token; a random one keeps it from looking real.
		token = core.NewSecretValue([]byte("sim-" + simToken))
	}
	if !token.IsZero() {
		files[l.Secret(areaControl, "github-token")] = append(token.Reveal(), '\n')
	}
	envs, err := in.runtimeEnvs(o, pw, controlSecrets{
		webhook: webhook, approvalKey: approvalKey, edgeAdmin: edgeAdmin, edgeCounters: edgeCounters,
		dispatcherAdmin: dispatcherAdmin, oidcClientSecret: o.Identity.OIDCClientSecret, githubClientSecret: o.Identity.GitHubClientSecret,
	}, statePW, token, signing.EncodePublicKey(pub))
	if err != nil {
		return err
	}
	for path, data := range envs {
		files[path] = data
	}
	for path, data := range files {
		if err := writeSecret(path, data); err != nil {
			return err
		}
	}
	return nil
}
