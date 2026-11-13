# Onboarding cycles 1-3 of the trial shop

Run with `examples/woocommerce-app/bin/onboard-cycle.sh <n>` on the Owner's Mac (arm64), 2026-11-13, against a fresh shop each time (new secrets, new volumes, bundle volume removed between cycles). Raw records: `plans/261113-1443-managed-application-deployment/onboarding-cycles.jsonl`.

## Result

- 3/3 cycles adopted, assembled, deployed, installed, seeded and verified the shop; 3/3 sandbox purchases succeeded.
- Adoption reproduced the committed `sisf-app.yaml` byte for byte each time.
- The bundle digest was identical in all three: `sha256:615cca73c91c5fba…` (reproducible assembly).
- The seeded data was identical: 100 products, 20 customers, 50 orders, 3 coupons, 2 shipping methods, gateways bacs/cod/sisf_sandbox, stock total 5981, orders checksum `3a15e53a464c…` in every cycle.
- All four checks passed in 1/3 cycles. The `home` check (bound 3000 ms) failed as too_slow in cycles 1 and 3. The other three checks passed in all three cycles.

| Cycle | Total | Bundle | Load vol. | Start | Install | Seed | Load avg (1/5/15) | home ms | purchase ms |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 22.1 min | 50 s | 51 s | 99 s | 105 s | 981 s | not recorded (89 at start) | 5648 (fail) | 5913 |
| 2 | 12.5 min | 57 s | 20 s | 47 s | 30 s | 568 s | 73 / 76 / 92 | 2676 | 6392 |
| 3 | 19.4 min | 169 s | 143 s | 68 s | 175 s | 562 s | 105 / 101 / 104 | 3777 (fail) | 3729 |

Median onboarding time: 19.4 min; seeding is 45-75% of it.

## Reading

- Every step time tracks host load, which comes from containers this run did not start (see `host-load-261113-1800-latency-checks.md`). Cycle 2 ran at the lowest load and was the only all-green one.
- The home page is the first uncached render after seeding. The bound was not relaxed and no warm-up request was added, since either would hide the slow first page a visitor would get.
- Onboarding success as defined in the brief (deployed, seeded, counts verified, purchase works) is 3/3. Check results are reported as measured, and the latency failures stand.

## Improvements

- Seeding creates orders one by one through WooCommerce; a bulk import would cut most of the time, but seeding is trial tooling, not product.
- `sisf app bundle` and the volume load are I/O bound on this disk; reusing a bundle volume by digest (what the compose backend already does) removes the load step on later deploys.
