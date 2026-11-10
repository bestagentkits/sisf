// Package conformance is the self-grading kit for backend authors and the
// capability matrix the grades are collected in.
//
// A backend is graded on four areas: lifecycle, isolation, rollback and
// observability. Grades come from what the backend declares about itself and,
// when one of the kits exercises it, from what it is then seen to do. A
// backend lacking any area is marked unsupported; support is never assumed
// from silence, from a claim alone, or from being hosted by a provider. A
// declaration alone is graded experimental at most: only a kit that took the
// backend through its whole interface can grade it supported, and nothing a
// backend or a caller writes down stands in for that run.
package conformance

import (
	"cmp"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Kind is the interface a backend implements.
type Kind string

const (
	// KindAgent is a coding or QA agent runtime.
	KindAgent Kind = "agent"
	// KindExecution is a sandbox provider for agent runs.
	KindExecution Kind = "execution"
	// KindDeploy stages, switches, observes and rolls back releases.
	KindDeploy Kind = "deploy"
)

// Valid reports whether k is an interface the kit can grade.
func (k Kind) Valid() bool { return k == KindAgent || k == KindExecution || k == KindDeploy }

// Declaration is what a backend states about itself before it is used.
type Declaration struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	// Modes lists the operating modes the backend can run in.
	Modes []manifest.Mode `json:"modes,omitempty"`
	// Capabilities is the backend's own statement; a capability it does not
	// mention counts as NOT_SUPPORTED.
	Capabilities core.CapabilitySet `json:"capabilities"`
}

// Verdict is the overall grade of a backend.
type Verdict string

const (
	// VerdictSupported means all four areas are verified and a kit exercised
	// the backend end to end without seeing a claim contradicted.
	VerdictSupported Verdict = "supported"
	// VerdictExperimental means no area is missing, but at least one is only
	// claimed or no kit has exercised the backend end to end.
	VerdictExperimental Verdict = "experimental"
	// VerdictUnsupported means at least one area is missing.
	VerdictUnsupported Verdict = "unsupported"
)

// Row is the grade of one backend: one line of the capability matrix.
type Row struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	// Areas holds a level for each of the four areas, always all four.
	Areas map[core.Capability]core.SupportLevel `json:"areas"`
	// Missing lists the areas graded NOT_SUPPORTED, in area order.
	Missing []core.Capability `json:"missing,omitempty"`
	// Notes say why an area was graded lower than the backend declared.
	Notes   []string `json:"notes,omitempty"`
	Verdict Verdict  `json:"verdict"`

	// exercised records that a kit took the backend through every step of
	// its interface. It is the evidence "supported" rests on. Only the kits
	// set it: it is not part of a declaration or of a probe and is not read
	// from JSON, so no backend can state it about itself.
	exercised bool
}

// Grade grades a backend from its declaration alone, which is a claim: the
// verdict is experimental at most, whatever the declaration says. The kits
// start from this grade, lower it when the backend's behavior contradicts
// its claims, and alone can raise it to supported.
func Grade(d Declaration) Row {
	row := Row{Kind: d.Kind, Name: d.Name, Areas: map[core.Capability]core.SupportLevel{}}
	for _, area := range core.ConformanceAreas() {
		row.Areas[area] = areaLevel(d.Kind, area, d.Capabilities)
	}
	row.settle()
	return row
}

// lower records that the backend failed a check of area and grades the area
// NOT_SUPPORTED: behavior outranks declaration.
func (r *Row) lower(area core.Capability, note string) {
	r.Areas[area] = core.SupportNotSupported
	r.Notes = append(r.Notes, string(area)+": "+note)
	r.settle()
}

// exercise records that a kit run took the backend through every step of its
// interface.
func (r *Row) exercise() {
	r.exercised = true
	r.settle()
}

// settle derives the missing areas and the verdict from the area levels.
func (r *Row) settle() {
	r.Missing = nil
	allVerified := true
	for _, area := range core.ConformanceAreas() {
		switch r.Areas[area] {
		case core.SupportSupported:
		case core.SupportUnverified:
			allVerified = false
		default:
			r.Missing = append(r.Missing, area)
		}
	}
	switch {
	case len(r.Missing) > 0:
		r.Verdict = VerdictUnsupported
	case allVerified && r.exercised:
		r.Verdict = VerdictSupported
	default:
		r.Verdict = VerdictExperimental
	}
}

// Matrix is the capability matrix: one row per backend, ordered by kind and
// then by name.
type Matrix []Row

// NewMatrix grades every declaration and returns the rows in a stable order.
func NewMatrix(declarations ...Declaration) Matrix {
	matrix := make(Matrix, 0, len(declarations))
	for _, d := range declarations {
		matrix = append(matrix, Grade(d))
	}
	slices.SortStableFunc(matrix, func(a, b Row) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return matrix
}

// Row returns the row of the named backend.
func (m Matrix) Row(kind Kind, name string) (Row, bool) {
	for _, row := range m {
		if row.Kind == kind && row.Name == name {
			return row, true
		}
	}
	return Row{}, false
}
