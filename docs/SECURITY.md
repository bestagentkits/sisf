# Security policy

SISF is at the design stage. This repository contains documents only, so there is no released software to patch yet. Reports about weaknesses in the design are welcome, because they are cheapest to fix now.

## Reporting a vulnerability

Report privately through GitHub: open the [Security tab](https://github.com/bestagentkits/sisf/security/advisories/new) and choose "Report a vulnerability". Do not open a public issue for a security problem.

Please include what an attacker could do, which part of the design it concerns (zone, interface or rule in the decision brief), and any steps or reasoning that show the problem.

## What to expect

- An acknowledgement after the report is read.
- A decision on whether the design changes, with the reasoning.
- Credit in the change, if you want it.

## Scope

In scope: the decision brief and architecture documents in `docs/`, and any code added to this repository later.

Out of scope: third-party services the design integrates with (GitHub, identity providers, model providers, hosting platforms). Report those to their owners.

The security requirements of the design are in section 7 of the [decision brief](self-improvement-software-factory-decision-brief.en.md).
