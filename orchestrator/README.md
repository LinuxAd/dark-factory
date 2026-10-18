# orchestrator

Go module for the factory's services. Phase 0 ships one binary,
`cmd/gateway`, the model gateway (SPEC.md §7.3, ADR 0001 Decisions D–F).

Nothing Go-related is installed on the host. Every target runs in a pinned
Docker image (tags at the top of the `Makefile`):

| Image | Used by |
|---|---|
| `golang:1.27.2` | `test`, `test-unit`, `build`, `tidy`, and the `Dockerfile` build stage (pinned there by `@sha256` digest) |
| `golangci/golangci-lint:v2.14.0` | `lint` (config in `.golangci.yml`) |
| `postgres:18.6` | `test` (throwaway database) |

```sh
make -C orchestrator test lint build   # the AC1/AC2 gate
make -C orchestrator test-unit         # no Postgres; integration tests SKIP
make -C orchestrator image             # factory-gateway:dev from Dockerfile
```

Module caches live in the Docker volumes `df-gomod`, `df-gocache` and
`df-golangci-cache`.

## How the integration tests reach Postgres

`make test` starts `postgres:18.6` in a container, waits for `pg_isready`,
then runs `go test` in a Go container that joins the Postgres container's
network namespace (`--network container:<pg>`) with
`GATEWAY_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:5432/postgres`.
Each test creates and drops its own database (`internal/testpg`). Without
that variable the integration tests skip with a message saying how to run
them. This was chosen over testcontainers-go because it needs no Docker
socket inside the test container and no host-override settings on Docker
Desktop.

## Layout

| Path | What |
|---|---|
| `cmd/gateway` | `gateway serve` (default) and `gateway mint-token` |
| `internal/gateway` | reverse proxy, token auth, header rules, metering, budget hook, probes, metrics |
| `internal/usage` | SSE/JSON usage parser; fixtures in `testdata/` (synthetic, see its README) |
| `internal/pricing` | per-model prices, integer nano-USD arithmetic |
| `internal/store` | pgx pool, goose migrations under an advisory lock, tokens, `agent_calls` |
| `internal/secrets` | Anthropic key from Secrets Manager (AWS SDK default chain) |
| `../memory` | SQL migrations, embedded by the separate module `github.com/LinuxAd/dark-factory/memory` (`go:embed` cannot reach outside a package's directory, so `memory/embed.go` lives next to the files and `go.mod` points at it with a `replace`) |

## Gateway runtime contract

| Item | Value |
|---|---|
| Binary | `/gateway` in the image built from `orchestrator/Dockerfile` (distroless `static-debian12:nonroot`, uid 65532, works with a read-only root FS) |
| `ANTHROPIC_KEY_SECRET_ID` | Secrets Manager secret id; its plain SecretString is the key (whitespace trimmed). Required |
| `DATABASE_URL` | Postgres DSN (empty: libpq `PG*` variables) |
| `LISTEN_ADDR` | `:8080`: proxy plus `GET /healthz` (always 200) and `GET /readyz` (200 after key fetch and migrations); probes need no token |
| `METRICS_ADDR` | `:9090`: `GET /metrics` |
| `UPSTREAM_URL` | `https://api.anthropic.com` |
| `AWS_REGION` | for the AWS SDK; credentials come from EKS Pod Identity |

Callers send `Authorization: Bearer <token>` (what the Agent SDK sends for
`ANTHROPIC_AUTH_TOKEN`). Any path is forwarded for a valid token; only
`POST /v1/messages` writes an `agent_calls` row.

Mint a job token (prints only the token on stdout):

```sh
kubectl exec deploy/gateway -n factory -- /gateway mint-token --run R --task T --stage S --ttl 1h
```

Metrics: `gateway_requests_total{code}`, `gateway_upstream_latency_seconds`,
`gateway_unpriced_calls_total`, plus Go and process collectors.
