# ADR 0015: Provider limits and outages

## Status

Status: proposed. Closes open point 6 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

Many parallel runs can exhaust GitHub API and model-provider rate limits, and a provider can go down.

## Decision

- A per-provider concurrency cap plus a token bucket.
- GitHub rate-limit headers are honored with backoff.
- A provider outage pauses the run behind a circuit breaker and is never recorded as success.

## Consequences

- Throughput is bounded by design.
- Paused runs need a visible state and a resume path.

## Acceptance test

Once accepted: a test with a failing fake provider proves the run pauses, the breaker opens, and no success is recorded.
