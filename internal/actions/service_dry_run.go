package actions

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// dryRun asks the handler what would change. It files nothing, audits
// nothing and changes nothing; a handler that cannot say is a bug.
func (s *Service) dryRun(ctx context.Context, call invocation, requiresApproval bool) (core.ActionResult, error) {
	call.req.ApprovalID = ""
	res, err := call.handle(ctx, call.req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if res.Plan == nil {
		return core.ActionResult{}, core.NewError(core.CodeInternal, "the action did not report what a dry run would change")
	}
	res.Plan.RequiresApproval = requiresApproval
	res.Output, res.AuditID = nil, ""
	return res, nil
}
