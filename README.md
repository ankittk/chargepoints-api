# EV Charge Points API

Take-home: small JSON REST API in Go to manage and query EV charge points (create, get by ID, nearby search).

## Build and run

```bash
go mod tidy
make run
# or: go run ./cmd/server
```

Server listens on `:8080` by default. Seed data loads into an empty SQLite DB.

### Local tracing

```bash
# Pretty-print spans to stdout (no collector needed):
OTEL_TRACES_EXPORTER=stdout make run

# Or send to a local OTLP HTTP collector (Jaeger all-in-one, Grafana Alloy, etc.):
OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318 make run
```

## API

Interactive docs (Swagger UI): [http://localhost:8080/docs](http://localhost:8080/docs)  
Raw spec: [`api/openapi.yaml`](api/openapi.yaml) (also [http://localhost:8080/openapi.yaml](http://localhost:8080/openapi.yaml)) — **generated** via `make openapi`.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/chargepoints` | Create charge point |
| `GET` | `/chargepoints/{id}` | Get by ID |
| `GET` | `/chargepoints/nearby?lat=&lon=&radius=` | Within radius (km) |
| `GET` | `/healthz` | Liveness |
| `GET` | `/readyz` | Readiness (DB ping) |

Example:

```bash
curl -s http://localhost:8080/chargepoints/nearby?lat=52.37\&lon=4.89\&radius=5 | jq
curl -s -X POST http://localhost:8080/chargepoints \
  -H 'Content-Type: application/json' \
  -d '{"name":"Dock 9","location":{"lat":52.37,"lon":4.89},"status":"AVAILABLE"}'
```

## Algorithms

### Nearby search (bounding box + Haversine)

We treat Earth as a sphere with radius **6371 km**. To answer “which chargers are within N km of this point?” we do two steps (`internal/geo` + store):

**Step 1 — bounding box (fast prefilter in SQL)**  
Build a rough rectangle around the search point so we do not Haversine every row in the database.

- About **111 km** equals **1 degree** of latitude.
- Longitude degrees shrink as you leave the equator, so we divide by `cos(latitude)`.
- Example: search at Amsterdam `(52.37, 4.89)` with `radius=5` km → latitude window about `±0.045°`, longitude window a bit wider (~`±0.07°`). SQL keeps only rows inside that box.

**Step 2 — Haversine (exact great-circle distance in Go)**  
For each candidate from the box, compute the shortest path over the sphere:

1. Convert latitudes and longitudes from degrees to radians.
2. From the difference in latitude and the difference in longitude, build the Haversine value `a`.
3. Turn that into a central angle with `2 * atan2(sqrt(a), sqrt(1 - a))`.
4. Multiply by 6371 to get distance in kilometres.
5. Keep the charge point only if `distance <= radius`.

Why both steps? The box is a **square-ish** cutout; its corners are farther than `radius`. Haversine keeps the true **circle**.

**Examples**

| Query | What happens |
|-------|----------------|
| `nearby?lat=52.37&lon=4.89&radius=5` | Seed chargers in central Amsterdam are inside; a charger in Rotterdam (~60 km) is outside. |
| Same point, `radius=0.01` | Almost nothing matches — very tight circle. |
| Two identical points | Haversine returns `0` km. |

**Limits:** if the box would cross the date line (±180° longitude) we clamp instead of wrapping, so searches there can miss points. Good enough for “chargers nearby”; not a full geodesic library. Later upgrade: PostGIS or S2.

## Test and lint

```bash
make test
make lint      # requires golangci-lint v2.12+
make openapi   # regenerate api/openapi.yaml from handler annotations
```

## Docker

```bash
make docker-build
make docker-run
```

## Kubernetes

Manifests in [`deploy/k8s/chargepoints.yaml`](deploy/k8s/chargepoints.yaml) (Namespace, PVC, Deployment ×1, Service). SQLite needs a single replica.

```bash
# build/load image into your cluster first, then:
kubectl apply -f deploy/k8s/chargepoints.yaml
kubectl -n chargepoints-api port-forward svc/chargepoints-api 8080:80
```

Update `image:` in the Deployment to your registry tag before applying to a shared cluster. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to your in-cluster collector if you use the OpenTelemetry Operator.

## Design decisions

### REST vs gRPC / Protobuf

The assignment specifies JSON HTTP endpoints (`POST/GET /chargepoints…`). That is a **public, curl-friendly contract**, so this service is **JSON REST**.

gRPC/protobuf would help for **internal** service-to-service calls (strong schemas, codegen, streaming charger status). Wire size/CPU is not the bottleneck here — nearby search cost is **SQLite I/O + Haversine**, not JSON encode. A gRPC gateway beside REST would add `.proto`, codegen, and dual error models without meeting the brief better.

**Later:** keep REST as the external API; add gRPC for fleet sync/streaming, reusing `pkg/chargepoint` + the store behind a new transport.

### SQLite vs Postgres

| | SQLite (chosen) | Postgres |
|--|-----------------|----------|
| Goal | Self-contained take-home | Multi-writer production |
| Ops | One file, no sidecar DB | Needs a server / operator |
| Concurrency | Single writer (`MaxOpenConns(1)` + file lock) | Strong concurrent writers |
| Geo | Bbox + Haversine in app | PostGIS / indexes |
| k8s | 1 replica + PVC | Stateless replicas OK |

SQLite (`modernc.org/sqlite`, pure Go, no CGO) makes `make run`, CI, and Docker trivial. Same repository interface can later target Postgres when multi-replica and spatial indexes matter.

### Why OpenAPI

- **Contract as code:** `swag` annotations on handlers → `make openapi` → `api/openapi.yaml` (no hand-edited drift).
- **Discoverability:** Swagger UI at `/docs` for humans; `/openapi.yaml` for clients/codegen.
- **Interview clarity:** shows how the HTTP surface is documented and kept in sync with handlers.

### Why `log/slog`

- Stdlib since Go 1.21 — **no extra logging dependency**.
- Structured JSON fields (`method`, `path`, `status`, `duration_ms`, `request_id`, `trace_id`, `span_id`).
- Levels: **Info** for access/lifecycle, **Error** for failures that become 5xx / startup abort.
- Prefer `slog` over zap/zerolog here: those win at extreme QPS; this API is not that workload.

### Why OpenTelemetry

- **Request correlation beyond logs:** HTTP span (`otelhttp`) + store spans (`Create` / `GetByID` / `Nearby` / `Ping`).
- W3C `traceparent` propagation for upstream/downstream services.
- Logs include `trace_id` / `span_id`; when unset, `X-Request-ID` falls back to the trace id so one key ties log lines to a trace.
- Exporters: `none` (default/CI quiet), `stdout` (local), `otlp` (collector / cluster operator).
- Probes `/healthz` and `/readyz` are **not** traced (noise).

### Other deliberate choices

- **Rate limit:** per-IP token bucket behind a mutex (map size capped); background eviction of idle IPs. Enable `TRUST_PROXY` only behind a trusted proxy.
- **Hardening:** body ≤ 64KiB, nearby radius ≤ 2000 km, reject NaN/Inf lat/lon, name length cap.
- **Lifecycle:** graceful shutdown on SIGINT/SIGTERM (HTTP drain + OTel flush).
- **Router:** Go 1.22+ `http.ServeMux` — no chi/gin/echo for a small surface.
