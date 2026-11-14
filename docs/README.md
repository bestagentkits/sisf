# SISF documentation

Where to find what. The code owns behaviour; these pages explain why, and say where to look and what to run.

## Use a factory

| Page | For |
|---|---|
| [Quickstart](guides/quickstart.md) | a simulated factory on your machine, signed in, with a first issue |
| [Installing a factory](guides/install.md) | modes and requirements, the development install, a real install (GitHub, sign-in, HTTPS), real agents, running as a service |
| [Trial factory on TOSE](guides/deploy-tose-trial.md) | hosting a trial factory in an unprivileged container (TOSE): the image, its health check, what such a platform cannot provide, rollback and removal |
| [Operations](guides/operations.md) | lifecycle commands, inspecting, surfaces, sign-in, candidate pull requests, reconciliation, public bug reports, budget, Decision Layer, secrets, held and interrupted releases, ports |
| [Policy, approvals and budget](guides/policy-and-approvals.md) | putting a policy in force, deciding approvals, step-up, changing the budget |
| [Why is my issue stuck?](guides/stuck-issues.md) | reading `sisf runs explain`, holds that stop the whole factory, causes of a block, retrying a Blocked issue |
| [Maintenance](guides/maintenance.md) | upgrade, back up, restore, renew keys, remove an install |
| [Troubleshooting](guides/troubleshooting.md) | real error messages and their fixes |
| [CLI reference](guides/cli-reference.md) | every `sisf` command and flag, and the Owner's `factoryd` host commands |
| [Incident runbook](guides/incident.md) | emergency stop and release, assessment, rollback, recovery |
| [Security guide](guides/security.md) | enforced controls, limits, the trust model of each mode |

## Decisions and design

| Page | For |
|---|---|
| Decision brief: [English](self-improvement-software-factory-decision-brief.en.md) · [Tiếng Việt](self-improvement-software-factory-decision-brief.md) (authoritative) | fixed product decisions, interfaces, the issue state machine, QA and release rules, acceptance tests SISF-01 to SISF-20 |
| [ADRs](adr/) | one decision each; open points are marked proposed |
| [Acceptance matrix](acceptance-matrix.md) | which test proves which acceptance criterion |
| [Capability matrix](capability-matrix.md) | which mode and backend may claim which capability |
| [Threat model](threat-model.md) | what the controls defend against |
| [Architecture](self-improvement-software-factory-architecture.html) and [architecture review](self-improvement-software-factory-architecture-review.html) | interactive design reference pages; download and open in a browser |
| [Security policy](SECURITY.md) | reporting a vulnerability |
| [Release notes: v0.1.0-alpha](release-notes/v0.1.0-alpha.md) | what the first alpha contains, its prerequisites, known limitations, upgrade and security notes |

The machine-readable contracts are [`api/openapi.yaml`](../api/openapi.yaml) (every action of the REST surface) and [`schemas/factory.schema.json`](../schemas/factory.schema.json) (the factory manifest).
