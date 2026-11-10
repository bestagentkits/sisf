# Capability matrix

Which deployment profile and backend can claim which capability. The profile list and conclusions come from brief §10; the grading kit that will compute this per backend is owned by `pkg/conformance`. This page holds the meaning and the rules, not per-backend results.

## Capabilities

| Capability | Meaning |
|---|---|
| `single_factory_deployment` | All three zones share one `factory_id`, manifest and lifecycle |
| `durable_tasks` | Run state survives restarts and resumes |
| `safe_exec` | Isolation for untrusted code is provable, including egress enforcement |
| `releasable_zone1` | Zone 1 can be staged, promoted and observed |
| `rollback` | A safe return to the last known good release exists |
| `observability` | Observed revision, metrics and logs are readable |

## Support levels

| Level | Meaning |
|---|---|
| supported | Passed conformance and end-to-end checks |
| experimental | Declared, partly verified, labeled honestly |
| unsupported | A required capability is missing; "deploy anywhere" is never claimed |
| `NOT_SUPPORTED` | An adapter operation (cancel, replay, usage) the provider cannot do; reported, never faked |
| `unverified` | Usage or guarantee that could not be measured |

## Rules

- A backend lacking lifecycle, isolation, rollback or observability is marked unsupported.
- A profile that cannot satisfy `single_factory_deployment`, `durable_tasks`, `safe_exec`, `releasable_zone1` and `rollback` is experimental or unsupported.
- A hosted provider is not an isolation guarantee. Untrusted code does not run automatically without proven isolation.
- A successful update API call never replaces an observed healthy release.
- A profile is raised to supported only after end-to-end proof, not by design.

## Current status

Design stage. No profile is verified yet. Targets: Docker Compose is the first mandatory profile; standalone local is the trusted-code profile. All others (Kubernetes, TOSE, Cloudflare, Vercel, Render, Railway) are later and stay unsupported until proven. The profile table is in brief §10; ADR [0001](adr/0001-three-zones-one-deployment.md) holds the invariant.
