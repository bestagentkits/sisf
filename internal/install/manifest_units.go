package install

import (
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// qaCredentialsRef is the credential set of the QA runner alone, so a coding
// agent that leaks its credentials cannot act as its own judge.
const qaCredentialsRef = "local:agent/qa-credentials"

const (
	minute = time.Minute
	hour   = time.Hour
)

// units declares the nine runtime units for a mode. The names, zones and
// lifecycles are the same in both modes; only the realization differs: a
// process with a command and a path in Standalone, an image in Docker.
func units(mode manifest.Mode) map[manifest.UnitName]manifest.Unit {
	if mode == manifest.ModeDocker {
		return dockerUnits()
	}
	return standaloneUnits()
}

// dockerUnits declares one container per unit. State and the sockets of the
// factory network are not reachable from the application's segment: the
// state store sits on the factory and control segments only.
func dockerUnits() map[manifest.UnitName]manifest.Unit {
	long, ephemeral := manifest.LifecycleLongRunning, manifest.LifecycleEphemeral
	return map[manifest.UnitName]manifest.Unit{
		manifest.UnitApp: {
			Image: "sisf-hello-app", Digest: unbuilt(artifactApp), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkApp}, Port: PortApp,
			Mounts: []manifest.Mount{{Volume: "app-data", Path: "/data"}},
		},
		manifest.UnitControl: {
			Image: "sisf-factoryd", Digest: unbuilt(artifactFactoryd), Command: []string{"factoryd", "control"}, Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkControl, manifest.NetworkFactory}, Port: PortControl,
			Public: true, PublicRoutes: []string{"/webhooks/github", "/report"},
			Mounts: []manifest.Mount{{Volume: "control-data", Path: "/var/lib/sisf/control"}},
		},
		manifest.UnitSupervisor: {
			Image: "sisf-supervisor", Digest: unbuilt(artifactSupervisor), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkControl}, Port: PortSupervisor,
			Mounts: []manifest.Mount{{Volume: "trust", Path: "/var/lib/sisf/trust"}},
		},
		manifest.UnitDispatcher: {
			Image: "sisf-factoryd", Digest: unbuilt(artifactFactoryd), Command: []string{"factoryd", "dispatcher"}, Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkFactory}, Port: PortDispatcher,
		},
		manifest.UnitRunner: {
			Image: "sisf-runner", Digest: unbuilt(artifactRunner), Lifecycle: ephemeral,
			Networks: []manifest.NetworkName{manifest.NetworkSandbox},
			Mounts:   []manifest.Mount{{Volume: "workspaces", Path: "/workspace"}},
		},
		manifest.UnitQARunner: {
			Image: "sisf-qa-runner", Digest: unbuilt(artifactQARunner), Lifecycle: ephemeral,
			CredentialsRef: qaCredentialsRef,
			Networks:       []manifest.NetworkName{manifest.NetworkSandbox},
			Mounts:         []manifest.Mount{{Volume: "qa-workspaces", Path: "/workspace"}},
		},
		manifest.UnitState: {
			Image: "postgres", Digest: PostgresDigest, Lifecycle: manifest.LifecycleDurable,
			Networks: []manifest.NetworkName{manifest.NetworkFactory, manifest.NetworkControl},
			Mounts:   []manifest.Mount{{Volume: "state-data", Path: "/var/lib/postgresql/data"}},
		},
		manifest.UnitArtifacts: {
			Lifecycle: manifest.LifecycleDurable,
			Mounts:    []manifest.Mount{{Volume: "artifacts", Path: "/var/lib/sisf/artifacts"}},
		},
		manifest.UnitEdge: {
			Image: "sisf-edge", Digest: unbuilt(artifactEdge), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkApp, manifest.NetworkControl, manifest.NetworkSandbox},
			Port:     PortEdge, Public: true,
		},
	}
}

// standaloneUnits declares one supervised process per unit, run from the
// install directory. The runner units are declared for completeness and are
// started by the dispatcher when agents run.
func standaloneUnits() map[manifest.UnitName]manifest.Unit {
	long, ephemeral := manifest.LifecycleLongRunning, manifest.LifecycleEphemeral
	return map[manifest.UnitName]manifest.Unit{
		manifest.UnitApp: {
			Command: []string{"bin/hello-app"}, Digest: unbuilt(artifactApp), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkApp}, Port: PortApp,
			Mounts: []manifest.Mount{{Volume: "app-data", Path: "data/app"}},
		},
		manifest.UnitControl: {
			Command: []string{"bin/factoryd", "control"}, Digest: unbuilt(artifactFactoryd), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkControl, manifest.NetworkFactory}, Port: PortControl,
			Mounts: []manifest.Mount{{Volume: "control-data", Path: "data/control"}},
		},
		manifest.UnitSupervisor: {
			Image: "bin/sisf-supervisor", Command: []string{"bin/sisf-supervisor"}, Digest: unbuilt(artifactSupervisor), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkControl}, Port: PortSupervisor,
			Mounts: []manifest.Mount{{Volume: "trust", Path: "data/trust"}},
		},
		manifest.UnitDispatcher: {
			Command: []string{"bin/factoryd", "dispatcher"}, Digest: unbuilt(artifactFactoryd), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkFactory}, Port: PortDispatcher,
		},
		manifest.UnitRunner: {
			Command: []string{"bin/sisf-runner"}, Digest: unbuilt(artifactRunner), Lifecycle: ephemeral,
			Networks: []manifest.NetworkName{manifest.NetworkSandbox},
			Mounts:   []manifest.Mount{{Volume: "workspaces", Path: "data/workspaces"}},
		},
		manifest.UnitQARunner: {
			Command: []string{"bin/sisf-qa-runner"}, Digest: unbuilt(artifactQARunner), Lifecycle: ephemeral,
			CredentialsRef: qaCredentialsRef,
			Networks:       []manifest.NetworkName{manifest.NetworkSandbox},
			Mounts:         []manifest.Mount{{Volume: "qa-workspaces", Path: "data/qa-workspaces"}},
		},
		manifest.UnitState: {
			Lifecycle: manifest.LifecycleDurable,
			Mounts:    []manifest.Mount{{Volume: "state-data", Path: "data/state"}},
		},
		manifest.UnitArtifacts: {
			Lifecycle: manifest.LifecycleDurable,
			Mounts:    []manifest.Mount{{Volume: "artifacts", Path: "data/artifacts"}},
		},
		manifest.UnitEdge: {
			Command: []string{"bin/sisf-edge"}, Digest: unbuilt(artifactEdge), Lifecycle: long,
			Networks: []manifest.NetworkName{manifest.NetworkApp, manifest.NetworkControl, manifest.NetworkSandbox},
			Port:     PortEdge, Public: true,
		},
	}
}
