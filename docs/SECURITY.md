# Security policy

SISF is pre-release software: there is no stable release yet, and some surfaces are still being integrated (see the [README](../README.md)). Reports about weaknesses in the design or the code are welcome. Controls the factory enforces today are described in the [security guide](guides/security.md).

## Reporting a vulnerability

Report privately through GitHub: open the [Security tab](https://github.com/bestagentkits/sisf/security/advisories/new) and choose "Report a vulnerability". Do not open a public issue for a security problem.

Please include what an attacker could do, which part of the design or code it concerns (zone, interface or rule in the decision brief), and any steps or reasoning that show the problem.

## What to expect

- An acknowledgement after the report is read.
- A decision on whether the design changes, with the reasoning.
- Credit in the change, if you want it.

## Scope

In scope: the code in this repository, the decision brief and the architecture documents in `docs/`.

Out of scope: third-party services the design integrates with (GitHub, identity providers, model providers, hosting platforms). Report those to their owners.

The security requirements of the design are in section 7 of the [decision brief](self-improvement-software-factory-decision-brief.en.md).
