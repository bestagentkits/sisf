package apispec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// File names of the generated documents inside the api directory.
const (
	OpenAPIFile = "openapi.yaml"
	LLMsFile    = "llms.txt"
)

// LLMs renders llms.txt: the same actions as the OpenAPI document, written
// for an agent to read before it calls anything.
func LLMs(specs []core.ActionSpec) []byte {
	var b bytes.Buffer
	b.WriteString("# SISF action API\n\n")
	b.WriteString("> " + header + "\n\n")
	b.WriteString("Every surface of the factory (dashboard, REST, CLI, MCP, WebMCP) calls one action service.\n")
	b.WriteString("An action's description documents it and never grants permission; roles and token scopes do.\n\n")
	b.WriteString("## Calling an action\n\n")
	fmt.Fprintf(&b, "- POST %s<name> with {\"input\": {...}, \"dry_run\": bool, \"idempotency_key\": \"...\", \"approval_id\": \"...\"}.\n", PathPrefix)
	b.WriteString("- Success: {\"output\": {...}, \"plan\": {...}, \"audit_id\": \"...\"}. A dry run returns plan only.\n")
	b.WriteString("- Failure: {\"error\": {\"code\", \"message\", \"next_actions\": [{\"action\", \"description\", \"params\"}]}}.\n")
	b.WriteString("  Branch on code, never on message, and pick the next step from next_actions.\n")
	b.WriteString("- Every write accepts dry_run, which reports exactly what would change and changes nothing.\n")
	b.WriteString("- idempotency_key: " + idempotencyKeyDescription + "\n\n")
	b.WriteString("## Sensitive actions: plan, approval, execute, audit\n\n")
	b.WriteString("1. Call without approval_id. The service files an approval request bound to the action, the exact input\n")
	b.WriteString("   and the policy in force, and answers APPROVAL_REQUIRED with details.approval_id.\n")
	b.WriteString("2. The Owner reads it with approvals.get and decides with approvals.decide after a fresh step-up.\n")
	b.WriteString("3. Call again with the same input and approval_id, after a fresh step-up. The approval is single use:\n")
	b.WriteString("   reuse is REPLAYED, changed input or a new policy revision is APPROVAL_REQUIRED.\n")
	b.WriteString("4. Every step is written to the audit log.\n")
	b.WriteString("An agent never acts as the Owner, even through an Owner's browser session.\n\n")
	b.WriteString("## Actions\n\n")
	for _, spec := range specs {
		roles := make([]string, len(spec.Roles))
		for i, r := range spec.Roles {
			roles[i] = string(r)
		}
		flags := string(spec.Sensitivity)
		if spec.StepUp {
			flags += ", step-up"
		}
		fmt.Fprintf(&b, "- %s [%s] %s\n  roles: %s; scopes: %s; POST %s%s\n", spec.Name, flags, spec.Summary,
			strings.Join(roles, ", "), strings.Join(spec.Scopes, ", "), PathPrefix, spec.Name)
	}
	b.WriteString("\n## Error codes\n\n")
	for _, c := range core.ErrorCodes() {
		b.WriteString("- " + string(c) + "\n")
	}
	b.WriteString("\nSchemas of every input and output: " + OpenAPIFile + "\n")
	return b.Bytes()
}

// Render returns both documents for specs.
func Render(specs []core.ActionSpec) (openapi, llms []byte, err error) {
	openapi, err = OpenAPI(specs)
	if err != nil {
		return nil, nil, err
	}
	return openapi, LLMs(specs), nil
}

// Generate writes both documents of the built-in registry into dir.
func Generate(dir string) error {
	reg, err := BuiltinRegistry()
	if err != nil {
		return err
	}
	openapi, llms, err := Render(reg.Specs())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, OpenAPIFile), openapi, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, LLMsFile), llms, 0o644)
}
