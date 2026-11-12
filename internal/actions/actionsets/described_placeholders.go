package actionsets

import (
	"context"
	"io/fs"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/core"
)

// describedStore stands in for the budget store when the budget actions are
// built only to read their specs. Their handlers are never called; if one
// were, it would refuse.
type describedStore struct{}

func (describedStore) View(context.Context, func(context.Context, budget.Ledger) error) error {
	return core.NewError(core.CodeNotSupported, "this budget store only describes actions")
}

func (describedStore) Update(context.Context, func(context.Context, budget.Ledger) error) error {
	return core.NewError(core.CodeNotSupported, "this budget store only describes actions")
}

// describedDocs stands in for the docs tree when the catalog actions are
// built only to read their specs.
type describedDocs struct{}

func (describedDocs) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
