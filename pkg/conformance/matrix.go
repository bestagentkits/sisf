// Package conformance is the self-grading kit for backend authors and the
// capability matrix the grades are collected in.
//
// A backend is graded on four areas: lifecycle, isolation, rollback and
// observability. Grades come from what the backend declares about itself and,
// when one of the kits exercises it, from what it is then seen to do. A
// backend lacking any area is marked unsupported; support is never assumed
// from silence, from a claim alone, or from being hosted by a provider.
package conformance

import (
	"cmp"
	"slices"

	"github.com/bestagentkits/sisf/internal/core"
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
	// EndToEndProven records that the backend also passed an end-to-end run.
	// Without it no backend is graded higher than experimental.
	EndToEndProven bool `json:"end_to_end_proven,omitempty"`
}

// Verdict is the overall grade of a backend.
type Verdict string

const (
	// VerdictSupported means all four areas are verified and an end-to-end
	// run proved them.
	VerdictSupported Verdict = "supported"
	// VerdictExperimental means no area is missing, but at least one is only
	// claimed or the end-to-end proof is still owed.
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

	endToEndProven bool
}

// Grade grades a backend from its declaration alone. The kits start from this
// grade and lower it when the backend's behavior contradicts its claims.
func Grade(d Declaration) Row {
	row := Row{Kind: d.Kind, Name: d.Name, Areas: map[core.Capability]core.SupportLevel{}, endToEndProven: d.EndToEndProven}
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
	case allVerified && r.endToEndProven:
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
