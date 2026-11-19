# Host load during the first check run

Measured 2026-11-13 17:47 and 18:00 (Asia/Saigon).

- `sisf app check --writes` against the spike shop (port 7480) failed: health too_slow, home unreachable, catalogue and purchase too_slow.
- Load averages: 261.68 / 256.01 / 214.21 at 17:47, then 181.66 / 192.17 / 196.63 at 18:00 (machine is arm64 Mac).
- Top CPU consumers are not containers this run started: `mongodb-mongo1-1` 324%, `studix-minio-1` 142%, `dewee-runtime-db-1` 69%, `goclaw-postgres-1` 38%, `dewee-app-db-1` 35%, ffmpeg 170%, Blender 89%.
- The rule forbids touching `studix-*` and `dewee-*`; nothing was stopped.
- Warm latencies under that load: healthz 0.7-1.2s, home 6.2s, Store API products 5.2s, a static file 0.3-0.9s. A static file at 0.9s points at the host, not WordPress.

Decision: keep the check bounds (5000 ms default) as they are. Re-measure when the load falls; report latency facts with the load at the time, and record any failed hourly sample in the 72h log as a host-caused gap rather than relaxing a bound.
