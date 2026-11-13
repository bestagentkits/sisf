// Package managedapp is the contract between an application the Owner already
// runs and the SISF factory that takes it over: what runs it, what its code is
// made of, where its data lives, what it depends on, how its behaviour is
// checked, and which of its changes only the Owner may approve.
//
// A Spec describes any stack. What is particular to one stack (WordPress, a Go
// program, ...) lives only in the detector that writes a Spec from what the
// Owner has (see the detect subpackage); nothing downstream branches on the
// stack's name.
//
// A revision of a managed application is a pair: the runtime image the Owner
// pinned, and a content bundle built from the repository by trusted code. The
// runtime image changes only when the Owner changes this Spec; the bundle
// changes with every candidate.
package managedapp

// APIVersion names this version of the contract.
const APIVersion = "sisf/managed-app/v1"

// FileName is the name of a Spec in an application's repository.
const FileName = "sisf-app.yaml"

// Spec is one managed application.
type Spec struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Name names the application; it prefixes every container and volume.
	Name string `yaml:"name" json:"name"`
	// Stack names the detector that wrote the Spec. It is informational:
	// nothing decides on it.
	Stack string `yaml:"stack" json:"stack"`

	Runtime      Runtime      `yaml:"runtime" json:"runtime"`
	Bundle       Bundle       `yaml:"bundle" json:"bundle"`
	Data         []Volume     `yaml:"data,omitempty" json:"data,omitempty"`
	Dependencies []Dependency `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	// Checks are the behaviour the Owner authorised: they must pass for a
	// revision to be released, and a check that passed on the last verified
	// release and now fails is a regression.
	Checks []Check `yaml:"checks" json:"checks"`
	Policy Policy  `yaml:"policy" json:"policy"`
}

// Runtime is the image every revision runs in, and how it is run.
type Runtime struct {
	// Image is pinned by digest: name@sha256:...
	Image string `yaml:"image" json:"image"`
	// Port is the port the application listens on inside its container.
	Port int `yaml:"port" json:"port"`
	// User is the uid:gid the container runs as; never root.
	User string `yaml:"user" json:"user"`
	// HealthPath answers 200 with a JSON object whose "revision" is the
	// revision the container was started as (SISF_REVISION).
	HealthPath string `yaml:"health_path" json:"health_path"`
	// Env is configuration that is not secret.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Command overrides the image's command; empty keeps it. It may name a
	// file of the bundle, such as a program the build exported.
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// Secrets maps a variable to the name of a secret. The variable
	// NAME_FILE is set to the path of a file holding the secret; the value
	// itself never enters the environment, the Spec or a log.
	Secrets map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

// Bundle is how a revision's content is made from the repository, in one of
// two ways. Lock: trusted code packages pinned archives and repository files,
// running nothing (an interpreted application such as WordPress). Program:
// the candidate's program is compiled in the build sandbox, which exports one
// file (a compiled application such as a Go service). Exactly one is set.
type Bundle struct {
	// Lock is the repository path of the lockfile pinning every downloaded
	// archive by version and sha256.
	Lock string `yaml:"lock,omitempty" json:"lock,omitempty"`
	// Program is the repository path of the package the sandbox builds.
	Program string `yaml:"program,omitempty" json:"program,omitempty"`
	// Mount is where the bundle is mounted, read-only, in the container.
	Mount string `yaml:"mount" json:"mount"`
	// Sources are files or directories of the repository copied into the
	// bundle, after the archives.
	Sources []Source `yaml:"sources,omitempty" json:"sources,omitempty"`
	// Prune are bundle paths removed after the archives are unpacked.
	Prune []string `yaml:"prune,omitempty" json:"prune,omitempty"`
}

// Source copies From (a repository path) to To (a bundle path).
type Source struct {
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
}

// Volume is a named volume that outlives every revision. A revision swap
// never touches its content.
type Volume struct {
	Name   string `yaml:"name" json:"name"`
	Target string `yaml:"target" json:"target"`
	// Owner is the uid:gid the volume's root is given once, when it is
	// created, so the non-root runtime can write to it.
	Owner string `yaml:"owner,omitempty" json:"owner,omitempty"`
}

// Dependency is a service the application needs that is not revisioned: a
// database, a cache. It runs on the application's network only, with
// credentials of its own, and its data survives every release and rollback.
type Dependency struct {
	Name    string            `yaml:"name" json:"name"`
	Image   string            `yaml:"image" json:"image"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Secrets map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Data    []Volume          `yaml:"data,omitempty" json:"data,omitempty"`
	// Ready is a command run in the dependency that exits 0 once it serves.
	Ready []string `yaml:"ready,omitempty" json:"ready,omitempty"`
	// Backup writes a complete dump of the dependency's data to its
	// standard output; Restore reads such a dump from its standard input.
	// Both run inside the dependency, under the supervisor's control only.
	Backup  []string `yaml:"backup,omitempty" json:"backup,omitempty"`
	Restore []string `yaml:"restore,omitempty" json:"restore,omitempty"`
}

// Policy is what the factory may do on its own.
type Policy struct {
	// Protected are repository path patterns a candidate may not change
	// without the Owner: the factory refuses such a candidate and blocks its
	// issue for the Owner. They add to the built-in ones; none can be removed.
	Protected []string `yaml:"protected,omitempty" json:"protected,omitempty"`
	// LockChanges says, per class of lockfile change, whether the factory
	// may release it on its own ("auto") or only with the Owner ("owner").
	// A class not named is "owner".
	LockChanges map[LockChange]Decision `yaml:"lock_changes,omitempty" json:"lock_changes,omitempty"`
	// AutoStartRegressions lets trusted intake start work on a detected
	// regression of a check without the Owner. Off unless stated.
	AutoStartRegressions bool `yaml:"auto_start_regressions,omitempty" json:"auto_start_regressions,omitempty"`
}

// LockChange is a class of change to the lockfile, judged against the
// Owner-approved baseline.
type LockChange string

const (
	LockPatch   LockChange = "patch"   // x.y.Z of an existing component
	LockMinor   LockChange = "minor"   // x.Y.z of an existing component
	LockMajor   LockChange = "major"   // X.y.z, or a version that is not semver
	LockAdded   LockChange = "added"   // a component the baseline does not have
	LockRemoved LockChange = "removed" // a component the candidate drops
	LockCore    LockChange = "core"    // any change to a core component
)

// Decision is who may release a class of change.
type Decision string

const (
	DecisionAuto  Decision = "auto"
	DecisionOwner Decision = "owner"
)

// Decide returns who may release a change of class c.
func (p Policy) Decide(c LockChange) Decision {
	if c == LockCore || c == LockAdded {
		return DecisionOwner // never the factory's alone, whatever the Spec says
	}
	if d, ok := p.LockChanges[c]; ok && d == DecisionAuto {
		return DecisionAuto
	}
	return DecisionOwner
}
