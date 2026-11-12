// Package actions is the one action service of the factory. Dashboard, REST,
// CLI, MCP and WebMCP all call Service.Invoke, so no surface holds power the
// others lack and a tool description never grants permission.
//
// A Registry holds every core.Action. Domain packages expose their actions
// as []core.Action from an Actions() function or constructor; only the
// integration package registers them, so this package imports no domain.
//
// Every call is authorized by core.Authorizer. A write supports dry_run,
// which reports exactly what would change and changes nothing. A sensitive
// write goes through plan -> approval -> execute -> audit: the first call
// files a single-use approval request bound to the action, its exact input
// and the policy in force and answers APPROVAL_REQUIRED; once the Owner
// approves, the same call with approval_id and a fresh step-up spends the
// approval and runs. Every failure is a typed *core.Error with next_actions.
//
// The OpenAPI document and llms.txt in api/ are generated from the specs of
// the registered actions by the apispec subpackage (go generate).
package actions
