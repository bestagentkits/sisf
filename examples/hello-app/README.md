# hello-app

The sample Zone 1 application of a factory deployment: the smallest app with a health check, metrics and a data volume to write to. A factory builds, publishes, observes and rolls back an app like this one. `sisf up` builds it as the application unit of an install, such as the one in the [quickstart](../../docs/guides/quickstart.md): from source in Standalone mode, from `build/hello-app.Dockerfile` in Docker mode.

## Endpoints

| Path | Answer |
|---|---|
| `GET /` | A greeting with a visit count kept in the data volume |
| `GET /healthz` | `{"revision":"…","status":"ok"}`, or 503 while unhealthy |
| `GET /metrics` | Prometheus text: `hello_up`, `hello_visits_total`, `hello_http_requests_total` |

## Settings

All come from the environment; the full list, including the faults used to exercise a release pipeline (off by default), is the doc comment of [main.go](main.go).

| Variable | Default |
|---|---|
| `HELLO_ADDR` | `127.0.0.1:7461` |
| `HELLO_DATA_DIR` | `./data` |
| `SISF_REVISION` | set by the deployment; reported by `/healthz` |

## Run it alone

The default port belongs to a running factory's blue slot, so pick another when a factory is up:

```bash
HELLO_ADDR=127.0.0.1:7481 HELLO_DATA_DIR="$(mktemp -d)" SISF_REVISION=dev go run ./examples/hello-app
curl http://127.0.0.1:7481/healthz     # in another terminal
```

## Test it

```bash
go test ./examples/hello-app/... -count=1
```

The `synthetic` package is the app's synthetic check suite, which a release monitor runs against a candidate while real traffic is too thin to judge it.
