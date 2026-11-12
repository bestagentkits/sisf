package control

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// IssueView is the stored webhook projection of one issue, for the tests.
type IssueView = issueProjection

// ReadIssueView reads the projection of ref from store.
func ReadIssueView(ctx context.Context, store state.Store, ref core.IssueRef) (IssueView, error) {
	var view IssueView
	err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		view, _, err = readProjection(ctx, tx, ref)
		return err
	})
	return view, err
}
