# Contributing

SISF is Apache-2.0 licensed, copyright AgentKit.best. Contribution rules are proposed in [ADR 0016](docs/adr/0016-oss-governance.md); the DCO requirement below applies now.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by` line certifying the [Developer Certificate of Origin](https://developercertificate.org/). Add it with:

```bash
git commit -s
```

The line must match the commit author, for example `Signed-off-by: Your Name <you@example.com>`. There is no CLA.

## Before you start

- Read the [decision brief](docs/self-improvement-software-factory-decision-brief.en.md) sections for your area and the [ADRs](docs/adr/).
- Do not reverse a product invariant. Propose a change through an ADR.
- Do not add code taken from another project without a provenance and license audit.

## Pull requests

- Branch from `main`; never push to `main`. Open a PR.
- Use conventional commit messages.
- Write the test first, with negative tests for security and spending rules.
- Interfaces under `pkg/` are experimental until 1.0; make changes additive.
- If you change one language of the decision brief, change the other in the same PR. A test checks that section numbers and IDs match.
- No secrets, tokens or personal data.

## Security

Report vulnerabilities privately; see [docs/SECURITY.md](docs/SECURITY.md).
