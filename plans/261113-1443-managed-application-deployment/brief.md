# Managed Application Deployment — confirmed brief (Owner Confirm 2026-11-13)

Objective: design, implement and verify Managed Application Deployment (`sisf adopt` + a general managed-app contract) with WordPress + WooCommerce as the first trial application, on this Mac.

## Done means
1. `sisf adopt` detects and generates a valid manifest for WP+Woo and for `examples/hello-app`, with tests.
2. WP+Woo on Docker (recorded ports), seeded: 100 products, 20 customers, 50 orders, coupons, stock, shipping methods, sandbox payment; counts checked with wp-cli.
3. 3/3 onboarding cycles from a clean state succeed, timed.
4. 100% of injected faults detected (5xx, latency, broken checkout, vulnerable plugin, failing update).
5. One good update released; 100% of hard-gate failures blocked or rolled back; order checksum before == after.
6. Sensitive changes (payment/checkout/tax, Core upgrade or DB migration, new plugin/permissions/users) blocked without signed approval; negative test.
7. Backup/restore and data preservation proven by checksum; sandbox purchase flow works.
8. At least one fix produced by a real AI agent, total cost < 20 USD, recorded.
9. 72h observation via launchd hourly + keep-awake; reports at 0h/24h/48h/72h with real numbers; gaps recorded.
10. Conformance and security boundaries checked; production-readiness assessment.
11. PR into main with docs, tests, reproduction scripts, report; build/test -race/lint green; isolated review clean; issue on bestagentkits/sisf.

## Out of scope
Kubernetes/serverless, TOSE, a general stack plugin system, real payments, real customer data, existing production.

## Constraints
New private repo `bestagentkits/sisf-trial-woocommerce`; AI budget 20 USD capped (API-equivalent cost reported by the CLIs; subscription logins); never touch `studix-*`/`dewee-*`; WP ports outside 7460-7469 and 7479 (WP on 127.0.0.1:7480); do not edit other sessions' areas; never weaken a security gate.

## Defaults
WPScan token absent: pinned public advisory snapshot in the trial repo. Sandbox payment: WooCommerce offline gateway + local fake gateway. Coder Claude, QA judge Codex.

## Stop only if
Destructive action outside brief; leaving scope/repo; spend > 20 USD; unexplained failure after advisor; invalid credential at use time.
