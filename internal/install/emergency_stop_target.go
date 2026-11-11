package install

import (
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// StopTarget is what an operator's emergency stop needs to know about one
// install, read from the install itself so the runbook needs no lookups:
// the factory to stop, the state store connection and the supervisor's
// admin address, and the root key that proves the supervisor is this
// install's.
type StopTarget struct {
	FactoryID core.FactoryID
	Mode      manifest.Mode
	// StateDSN is the control zone's store connection. It is a secret.
	StateDSN core.SecretValue
	// AdminAddr is the supervisor's admin address as seen from the host.
	AdminAddr string
	// RootKeyID names the install's root key.
	RootKeyID string
}

// ReadStopTarget reads the stop target of the install in dir. Docker mode
// keeps the supervisor's admin socket and the state store inside the
// Compose project, out of the host's reach, so it is refused with the
// routes that work there.
func ReadStopTarget(dir string) (StopTarget, error) {
	in := &Installer{Layout: Layout{Dir: dir}}
	m, err := in.Layout.LoadManifest()
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return StopTarget{}, core.NewError(core.CodeNotFound, dir+" holds no install (no factory.yaml)",
				core.NextAction{Action: "sisf emergency-stop --factory-id ID", Description: "name the factory instead, with " + "SISF_STATE_DSN and --admin"})
		}
		return StopTarget{}, err
	}
	t := StopTarget{FactoryID: m.FactoryID, Mode: m.Mode, RootKeyID: in.rootKeyID()}
	if m.Mode == manifest.ModeDocker {
		return t, core.NewError(core.CodeNotSupported,
			"the install in "+dir+" runs in Docker mode: its supervisor's admin socket and its state store are inside the Compose project, out of this command's reach",
			core.NextAction{Action: "sisf call factory.emergency_stop --input '{\"reason\":\"...\"}'", Description: "stop it through control, as the Owner"},
			core.NextAction{Action: "dashboard", Description: "Overview, Emergency stop"})
	}
	env, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
	if err != nil {
		return t, err
	}
	if dsn := env["SISF_STATE_DSN"]; dsn != "" {
		t.StateDSN = core.NewSecretValue([]byte(dsn))
	}
	t.AdminAddr = supervisor.DefaultAdminAddr
	return t, nil
}
