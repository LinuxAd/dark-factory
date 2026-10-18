# orchestrator

> **Accepted for Phase 0, fix before Phase 1** (ADR 0001):
>
> - Server tools and MCP connectors in request bodies get around the egress
>   allowlist: the gateway forwards bodies untouched, so `web_fetch`,
>   `web_search` and `mcp_servers` make Anthropic reach the internet on the
>   agent's behalf (security review M1).
> - The `anthropic-beta` header is forwarded unfiltered, so a caller can turn
>   on any beta feature.
> - The gateway starts listening before Init (key fetch, database,
>   migrations) finishes; `/readyz` returns 503 until then, and the proxy
>   answers 503 too, so traffic is gated by readiness, not by the listener.
> - The `agent_calls` row is written before the response body is closed, so
>   the end of every `POST /v1/messages` response waits for that insert (up
>   to 10 s if Postgres is slow).

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
| `cmd/gateway` | `gateway serve` (default), `gateway mint-token` and `gateway revoke-token` |
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
| `UPSTREAM_URL` | `https://api.anthropic.com`; must be `https` (startup fails otherwise) |
| `GATEWAY_ALLOW_INSECURE_UPSTREAM` | unset; `1` allows an `http://` `UPSTREAM_URL`, for local fakes only |
| `GATEWAY_MAX_INFLIGHT_PER_TOKEN` | `8`: concurrent requests per job token; more get 429 `rate_limit_error` with no upstream call |
| `AWS_REGION` | for the AWS SDK; credentials come from EKS Pod Identity |

Callers send `Authorization: Bearer <token>` (what the Agent SDK sends for
`ANTHROPIC_AUTH_TOKEN`). Only `POST /v1/messages`,
`POST /v1/messages/count_tokens`, `GET /v1/models` and `GET /v1/models/{id}`
are forwarded; any other method or path (Batches, Files, a trailing slash, an
escaped path) gets 404 `not_found_error` with no upstream call. Only
`POST /v1/messages` writes an `agent_calls` row.

Limits: request headers 64 KiB (431 above), idle keep-alive 90 s, header
read 10 s, request body 32 MiB (413 `request_too_large` above, no row). An
unknown token is remembered for 30 s (at most 10,000 entries) so repeated
bad tokens get 401 without a database lookup. That cache does not absorb a
flood of distinct random tokens: each is a Postgres lookup. There is no
global rate limiter. The load on Postgres is bounded by the pgx pool size
(pgx default `max(4, NumCPU)`, or `pool_max_conns` in `DATABASE_URL`), which
queues lookups beyond its connections, plus the per-token in-flight cap
(taken after the lookup) on what valid tokens add.

Mint a job token (prints only the token on stdout; `--ttl` defaults to 1h
and above 2h is rejected):

```sh
kubectl exec deploy/gateway -n factory -- /gateway mint-token --run R --task T --stage S --ttl 1h
```

Revoke every token of a run (sets `revoked_at = now()` on its unrevoked
tokens, prints the number revoked, e.g. `2`, and exits 0 even when that is
`0`; the next request with a revoked token gets 401):

```sh
kubectl exec deploy/gateway -n factory -- /gateway revoke-token --run R
```

Metrics: `gateway_requests_total{code}`, `gateway_upstream_latency_seconds`,
`gateway_unpriced_calls_total`, plus Go and process collectors.
