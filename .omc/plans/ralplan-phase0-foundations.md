# Phase 0 — Foundations: consensus plan

Status: **approved, in execution** (v4 amendments from team-verify; v3: Critic APPROVE on v2, Architect SOUND-WITH-CHANGES on v2; required changes from both folded in, see §10). Mode: RALPLAN-DR **deliberate** (credential isolation and data durability are in scope).
Source: `SPEC.md` §1, §6.5, §7, §9 (Phase 0 row), §10. Decisions in memory `plan-review-decisions` are settled and not reopened.

## 1. Requirements summary

Phase 0 builds the minimum platform under the factory. It builds no pipeline stage.

**Exit criterion (SPEC.md §9, verbatim):** a sandbox Job calls Claude through the gateway with no key in the pod, the call appears in `agent_calls`, and the row is still there after `make down` then `make up`.

In scope (SPEC.md §9): repo skeleton, `CLAUDE.md`, ADRs, `01-baseline` additions, CloudNativePG cluster with S3 backup and restore, `agent_calls`, gateway, `sandbox` pool, egress proxy.

Out of scope: webhooks, reconciler, stages, the orchestrator-internal `X-Factory-*` caller path (Phase 1), budget enforcement (Phase 3; Phase 0 leaves a no-op hook), dashboards, verify image, the GitHub App / webhook / Discord secret containers (Phase 1).

### Codebase facts this plan relies on (all paths in `~/repos/platform-project`)

| Fact | Where |
|---|---|
| Baseline layer persists through `make down`; holds Secrets Manager containers seeded by `make secrets` | `terraform/01-baseline/main.tf:1-10`, `secrets.tf` |
| `make down-all` destroys baseline with `-var force_destroy=true` | `Makefile:80-93` |
| Pod Identity roles live in the platform layer | `terraform/02-platform/iam.tf:16-191` |
| External Secrets role reads every `${var.name}/*` secret; the only store is a `ClusterSecretStore` with no `conditions` | `terraform/02-platform/iam.tf:152-191`, `gitops/charts/platform-bootstrap/templates/secret-stores.yaml:1-12` |
| VPC CNI is configured with prefix delegation only — **NetworkPolicy is not enforced today** | `terraform/02-platform/eks.tf:13-24` |
| IMDS hop limit 1 on nodes (pods cannot reach node credentials) | `terraform/01-baseline/guardrails.tf:30`, `gitops/charts/karpenter-nodes/templates/ec2nodeclass.yaml:24-28` |
| CNPG operator chart 0.29.1 in `cnpg-system`; cert-manager installed | `gitops/bootstrap/addons/cloudnative-pg.yaml:32-36`, `gitops/bootstrap/addons/cert-manager.yaml` |
| One hard-coded `default` NodePool in the local `karpenter-nodes` chart | `gitops/charts/karpenter-nodes/templates/nodepool.yaml:1-44` |
| `services` AppProject: owner repos only; cluster-scoped kinds limited to `Namespace`; all namespaced kinds allowed; `factory` not protected | `gitops/charts/platform-projects/templates/services.yaml:11-24`, `values.yaml:7-20` |
| Services ApplicationSet: nested registration file `service: {name, repoURL, revision?, path?}`; passes only `image.repository`, `image.tag`, `route.host`; `CreateNamespace=true` without metadata; `selfHeal`; `retry.limit: 10` | `gitops/bootstrap/services.yaml:1-85`, `gitops/services/README.md` |
| Teardown: wake (1), stop AppSet controller (2), delete workload Apps (3), delete every PVC (4), edge (5), nodes (6), controllers (7) | `scripts/teardown.sh:10-31,138-172` |
| Sleep deletes NodePools, so Postgres is down while asleep | `scripts/sleep.sh:85-87` |
| CI role trusts only `refs/heads/main` pushes | `terraform/01-baseline/main.tf` (`github_oidc_subjects`) |
| kube-prometheus-stack selects ServiceMonitors from all namespaces | `gitops/addons/kube-prometheus-stack/values.yaml:57-61` |

`dark-factory/` today holds only `SPEC.md` and a git repo with no commits.

## 2. RALPLAN-DR summary

### Principles
1. **The exit criterion is the only finish line.** Anything not needed to show it on the real cluster waits for its phase.
2. **One credential holder, enforced by IAM and the network, not by convention.** The Anthropic key exists in Secrets Manager and in the gateway process's memory only — not in etcd, not in any env var or volume of another pod.
3. **Durability lives outside the cluster and survives any length of downtime.** Nothing that ages out while the platform is down may be the only restore point.
4. **Reuse the platform's patterns before inventing new ones**; where a factory need forces a platform change, make the change generic.
5. **Tests prove behaviour at seams.** Gateway logic against a fake upstream; isolation and durability on the real cluster.

### Decision drivers (top 3)
1. Time to a working exit criterion (1 weekend, SPEC.md §9).
2. `make up` restores the row after a `make down` of any duration, and the very first bring-up works.
3. Key and egress isolation must be demonstrable by a probe that fails loudly.

### Decision A — where cluster-scoped and IAM pieces live

| Option | Pros | Cons |
|---|---|---|
| **A1 (chosen): platform-project owns cluster-scoped + IAM + CNI pieces, made generic; `dark-factory/deploy/chart` is namespaced only**, registered by `gitops/services/factory.yaml` | Teardown tiers stay correct (NodePool in `nodes`, factory in `workload`, ADR 0004); no AppProject widening; IAM next to the cluster | Cross-repo PR for Phase 0; mitigated by making each change generic (`extraPools`, pre-teardown backups) so the platform gains features, not factory special-cases |
| A2: new `factory` AppProject + own Application allowed cluster-scoped kinds | Factory self-contained | NodePool would sit in the `workload` tier and be deleted before PVC/LB handling, breaking teardown ordering; IAM must be Terraform anyway; widens what a non-platform repo can create |

### Decision B — CloudNativePG backup and bring-up

Backup mechanism:

| Option | Pros | Cons |
|---|---|---|
| **B1 (chosen): Barman Cloud plugin in `cnpg-system` + `ObjectStore` CR with `retentionPolicy: "30d"`** | Supported path (in-tree `barmanObjectStore` deprecated since CNPG 1.26); chart 0.29.1 ships a newer operator; cert-manager present; Barman retention always keeps the newest backup | New add-on; plugin sidecar PSA/boto3 behaviour to confirm (Step 0) |
| B2: in-tree `barmanObjectStore` | Fewer parts | Deprecated, forced migration on the one component that must not lose data. Kept as the time-boxed fallback (pre-mortem 3) |

Bring-up model (CNPG cannot "recover if a backup exists, else initdb", and refuses to archive into a non-empty WAL path by default):

| Option | Pros | Cons |
|---|---|---|
| **B-i (chosen, hardened): chart Cluster always `bootstrap.recovery`, fixed `serverName: factory-db`, annotation `cnpg.io/skipEmptyWalArchiveCheck: enabled`; first-ever bring-up via `make db-init` using a seed Cluster `factory-db-seed`** | Static chart; no change to the shared ApplicationSet; recovery follows the latest timeline and WAL names carry the timeline, so a recovered cluster does not overwrite its predecessor | Disables the two-writer guard; safe only with one writer. Enforced: `db-init` refuses on a non-empty prefix and deletes the seed before Argo's Cluster starts recovering |
| B-ii: new `serverName` per bring-up | Follows CNPG guidance; each incarnation isolated | Needs either a factory-specific value in the shared services ApplicationSet or a git commit per `make up` (breaks unattended bring-up). Fallback if Step 0 shows the plugin ignores the skip annotation |

### Decision C — egress proxy

| Option | Pros | Cons |
|---|---|---|
| **C1 (chosen): Smokescreen (Stripe), built from a pinned commit in `runner/egress.Dockerfile`, hostname allowlist = SPEC.md §7.2.** Sandbox clients present no mTLS certificate, so Smokescreen runs with its allow-missing-role option and an ACL whose `default` role is `{action: enforce, allowed_domains: [SPEC.md §7.2 list, incl. *.githubusercontent.com]}`. Step 0a confirms the VPC and pod CIDRs fall in its default deny ranges (add `--deny-range` otherwise) | Purpose-built egress proxy; runs non-root (PSA `restricted` clean); denies private, link-local and metadata IPs by default, so the proxy cannot be used to reach Postgres or IMDS | Less familiar; image built by us |
| C2: Squid `dstdomain` allowlist | Mature, detailed deny logs | Common images start as root; non-root under `restricted` needs custom pid/spool/log config; no private-IP blocking without extra ACLs |
| C3: FQDN NetworkPolicy | No proxy hop | Needs Cilium or similar; VPC CNI policy has no FQDN rules — invalid |
| C4: small in-house Go CONNECT proxy | One language | Writing a security boundary instead of reusing one (Principle 4) |

### Decision D — usage capture

| Option | Pros | Cons |
|---|---|---|
| **D1 (chosen): `httputil.ReverseProxy`; `ModifyResponse` wraps the body in a tee that feeds an SSE/JSON usage parser in its own package (`internal/usage`)** | Byte-for-byte pass-through; streaming preserved (ReverseProxy flushes `text/event-stream` immediately); parser isolated so new usage fields touch one package | We own SSE parsing; fixtures from real responses required |
| D2: buffer then forward | Simple | Breaks streaming for the Agent SDK — invalid |

Accounting rules: the gateway forwards only an allowlist — `POST /v1/messages`, `POST /v1/messages/count_tokens`, `GET /v1/models`, `GET /v1/models/{id}` — and answers anything else 404 without an upstream call (v4: Batches and Files would otherwise be unmetered and workspace-wide). Rows only for `POST /v1/messages`; the other allowed paths write no row. Input and cache tokens from `message_start.message.usage`; then every non-null field of the **last** `message_delta.usage` overwrites (these are cumulative, never summed). Outbound `Accept-Encoding` is stripped so the parser always sees plain bytes. The row write uses `context.WithoutCancel` so client cancels still record (`http_status = 499`).

### Decision E — key delivery to the gateway

| Option | Pros | Cons |
|---|---|---|
| **E1 (chosen): gateway reads the secret at startup with the AWS SDK via its own Pod Identity role (`GetSecretValue` on one ARN); shared External Secrets role gets an explicit `Deny` on `${name}/factory/*`** | Key never in etcd; no other namespace can obtain it through ESO; isolation is IAM-enforced (Principle 2) | Departs from the ESO pattern (P4 tension, accepted because driver 3 outranks it); rotation needs a restart |
| E2: namespaced `SecretStore` with its own role + ESO `Deny` | Keeps ESO pattern | Key still lands in a Secret in etcd, readable by anything that can mount Secrets in the namespace |
| E3: ExternalSecret through the existing `ClusterSecretStore` | Zero new IAM | Any services-project namespace can create an ExternalSecret for the key — invalid |

### Decision F — migrations

**Chosen:** `goose` with SQL in `memory/migrations/`, embedded (`embed.FS`) in the gateway binary, run at gateway startup under `pg_advisory_lock`; readiness probe fails until migrations finish. Rejected: Argo PreSync hook (runs before the CNPG `Cluster` exists on every fresh sync — deadlock); Sync-wave hook after the Cluster (works, but adds a Job and a wait loop for no gain in Phase 0).

### Decision G — pre-teardown backup

| Option | Pros | Cons |
|---|---|---|
| **G1 (chosen): generic step "1b" in platform `scripts/teardown.sh`, after wake (step 1) and before step 2. For every CNPG `Cluster` labelled `platform/backup-before-teardown: "true"`: wait up to `TEARDOWN_TIMEOUT_BACKUP` for it to be healthy (Karpenter may still be bringing nodes back after wake); if it is not healthy **and** its object store (the WAL-archiver plugin's ObjectStore `status.serverRecoveryWindow`, not Backup CRs, which do not survive `make down`/`make up`) reports no backup (uninitialised, e.g. before `db-init`), skip it at once with a loud warning; otherwise create an on-demand plugin `Backup`, wait for `completed`, then `kubectl exec -c postgres … pg_switch_wal()` and wait for `pg_stat_archiver.last_archived_wal` to reach it. If a healthy Cluster's backup fails or times out, exit non-zero and print exactly one command, `FORCE=yes make down`; `FORCE` is passed through the `down` and `down-all` targets. Teardown's early-exit paths (API unreachable, no Argo CD, `teardown.sh:91-98`) print that the pre-teardown backup was skipped** | Every `make down` is safe whichever repo it is run from; wake already handled; teardown keeps all ordering in one place (P4) | Platform repo gains a CNPG-aware step (generic, label-driven) |
| G2: `dark-factory` `make down` wrapper | Factory concern stays in factory repo | Running `make down` in platform-project skips it; fails on a sleeping platform |

## 3. Implementation steps

**Step 0 — Spikes (time-box: 3 hours total, before any other step)**
- 0a Read the pinned `plugin-barman-cloud` docs/source: confirm (i) it honours `cnpg.io/skipEmptyWalArchiveCheck`, (ii) its sidecar securityContext passes PSA `restricted`, (iii) its boto3 is ≥ 1.34.41 (EKS Pod Identity credentials). Any "no" → record in ADR and switch to the fallback named in Decision B. Also confirm Smokescreen's default deny ranges cover the VPC and pod CIDRs (Decision C1).
- 0b Capture real fixtures: one non-stream and one streaming `/v1/messages` response, plus one with server-tool usage, saved under `orchestrator/internal/usage/testdata/` (key from Adam's shell, not committed).

**Step 1 — Repo skeleton and rules** (`dark-factory`)
- `CLAUDE.md` from SPEC.md §1, §4.1, §10.
- `docs/adr/0001-phase0-foundations.md` (Decisions A–G + Step 0 outcomes), `docs/adr/README.md`.
- `orchestrator/go.mod`, `orchestrator/cmd/gateway/`, `orchestrator/internal/{gateway,usage,pricing,store}/`.
- `memory/migrations/`, `runner/`, `deploy/chart/`, `scripts/`, `Makefile`.
- `.claude/settings.json` hooks: `golangci-lint run`, `go test ./...`, `terraform validate` (SPEC.md §10).
- `.github/workflows/ci.yml`: lint, test; on `main` only, build and push `…/services/factory` (gateway), `…/services/factory-runner`, `…/services/factory-egress` tagged with the commit SHA. Images exist only after Adam merges to `main`; first bring-up depends on that merge.

**Step 2 — platform-project PR** (merged by Adam before Step 5 is deployed)
- 2a `terraform/01-baseline/factory.tf`: bucket `${var.name}-factory-state-${account_id}-${region}` (the existing naming convention, `terraform/01-baseline/cloudtrail.tf:6`; the same literal goes into `deploy/chart/values.yaml`) (versioning; SSE-KMS with a dedicated CMK whose key policy delegates to account IAM so recreated roles keep access; public access block; `force_destroy = var.force_destroy` so `down-all` stays the one deliberate wipe; lifecycle: **only** noncurrent-version expiry after 30 days, no current-object expiry). Secret container `${name}/factory/anthropic-api-key` only. Outputs: bucket, key ARN. `terraform test`: versioning enabled, no lifecycle rule with current-object expiration, `force_destroy` defaults false, bucket name lowercase and ≤ 63 characters.
- 2b `terraform/02-platform/iam.tf`: Pod Identity role `factory-db` bound to SAs `factory-db` and `factory-db-seed` in ns `factory` (S3 Get/Put/Delete on `cnpg/*`, `s3:ListBucket` on the bucket ARN without a prefix condition — Barman's HeadBucket carries no `s3:prefix`, and the bucket is dedicated to the factory — KMS); role `factory-gateway` bound to SA `gateway` in ns `factory` (`secretsmanager:GetSecretValue` on the one ARN, `kms:Decrypt` via secretsmanager). ESO role policy: explicit `Deny` on `${name}/factory/*`.
- 2c `terraform/02-platform/eks.tf`: `vpc-cni` `configuration_values` adds `enableNetworkPolicy = "true"`. ADR in platform-project noting all namespaces are unaffected until they declare policies, and that rolling back is the same one-line `configuration_values` change.
- 2d `gitops/charts/karpenter-nodes`: generic `extraPools` list (name, labels, taints, capacityTypes, limits); values add `sandbox` (spot, taint `platform/sandbox=true:NoSchedule`, label `platform/nodegroup: sandbox`, `consolidateAfter: 1m`, cpu limit 16).
- 2e `gitops/bootstrap/addons/plugin-barman-cloud.yaml` + `gitops/addons/plugin-barman-cloud/values.yaml`: installs into `cnpg-system`, tier `controller`, pinned version from Step 0a.
- 2f `scripts/teardown.sh`: Decision G1 step "1b", with `scripts/tests/teardown.test.sh` cases for healthy-backup-ok, healthy-backup-fails (aborts and prints the `FORCE` command) and uninitialised (skips with a warning). `Makefile`: pass `FORCE` through `down`/`down-all`. `scripts/lib.sh`/`preflight.sh`/`seed-secrets.sh`: an **optional** secret list that is checked and seeded only when its container exists, so a platform `make up` without the factory never fails on a missing Anthropic key (`preflight.sh:133-143`, `lib.sh:158-164`).
- 2h (v4) `gitops/charts/platform-projects`: `factory` and `factory-sandbox` added to `protectedNamespaces`; a new AppProject `factory` (source repo dark-factory only, destinations those two namespaces, cluster-scoped kinds `Namespace` only); the services ApplicationSet reads an optional `project` key (default `services`). Without this, any registered service could deploy a pod as SA `gateway` in `factory` and read the key through its Pod Identity.
- 2g `gitops/services/factory.yaml`:
  ```yaml
  service:
    name: factory
    repoURL: https://github.com/<owner>/dark-factory
    path: deploy/chart
    project: factory
  ```

**Step 3 — Schema** (`memory/migrations/0001_agent_calls.sql`, `0002_gateway_tokens.sql`)
- `agent_calls` per SPEC.md §6.5: `id bigserial`, `run_id text`, `task_id text null`, `stage text`, `model text`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `server_tool_requests` (int), `cost_usd numeric(12,6) null`, `latency_ms int`, `http_status int`, `transcript_s3_key text null`, `created_at timestamptz default now()`. `run_id` is text without FK until `runs` exists (Phase 1 migration adds it).
- `gateway_tokens`: `token_sha256 bytea pk`, `run_id`, `task_id`, `stage`, `expires_at`, `revoked_at`.
- `internal/pricing`: per-model table with a unit test per model in use; unknown model → `cost_usd` NULL + `gateway_unpriced_calls_total` increment, never a failed call.

**Step 4 — Gateway** (`orchestrator/internal/gateway`, `cmd/gateway`)
- Startup: fetch key (Decision E1), run migrations (Decision F), then listen. The key fetch and the DB connection retry with exponential backoff (capped at 30 s) rather than exiting, because a CNPG recovery takes minutes.
- Auth: `Authorization: Bearer <job token>` (what the Agent SDK sends for `ANTHROPIC_AUTH_TOKEN`) → hashed lookup; valid, unexpired, unrevoked → run/task/stage from the token. The token is authoritative: any `X-Factory-*` headers on a token request must match it or the request is rejected 403. Requests with no valid token → 401, no upstream call. (The orchestrator-internal header path is Phase 1.)
- Outbound: strip inbound `authorization`, `x-api-key`, `accept-encoding`; set `x-api-key`; forward everything else untouched (beta headers, cache control, server tools).
- Usage capture and row write per Decision D. One row per upstream HTTP request.
- `Budget` interface, no-op implementation, called before forwarding.
- Logs: method, path, status, latency, run/task/stage, model — never headers or bodies. Metrics: `gateway_requests_total{code}`, `gateway_upstream_latency_seconds`, `gateway_unpriced_calls_total`; ServiceMonitor in chart.
- `gateway mint-token --run --task --stage --ttl` subcommand, run as `kubectl exec deploy/gateway -n factory -- gateway mint-token …` (uses the pod's DB connection). Replaced by the orchestrator in Phase 1.

**Step 5 — Chart** (`deploy/chart`, namespaced only)
- Renders `Namespace` objects `factory` and `factory-sandbox` with `pod-security.kubernetes.io/enforce: restricted`. Sandbox Jobs run in `factory-sandbox`, which holds no Secrets except per-Job tokens, so no Job template can mount the DB credentials.
- Image names derived from the injected `image.repository`: gateway = `{{ .Values.image.repository }}`, runner = `…-runner`, egress = `…-egress`, all at `image.tag`.
- `factory`: gateway Deployment/Service/SA `gateway`, ServiceMonitor; CNPG `Cluster` `factory-db` (`instances: 1` for Phase 0, label `platform/backup-before-teardown: "true"`, annotation `cnpg.io/skipEmptyWalArchiveCheck: enabled`, `bootstrap.recovery` from the ObjectStore with `database: factory`, `owner: factory`), `ObjectStore` (bucket name in `values.yaml`, `inheritFromIAMRole: true`, `retentionPolicy: "30d"`), nightly `ScheduledBackup`; Smokescreen Deployment/Service/ConfigMap.
- NetworkPolicies, default-deny ingress+egress in both namespaces, then allow only:
  - all pods → kube-dns UDP/TCP 53;
  - `factory-sandbox` pods → `smokescreen:4750`, `gateway:8080`;
  - gateway → Postgres 5432, 443 to `0.0.0.0/0` (Anthropic, Secrets Manager), and the EKS Pod Identity agent `169.254.170.23/32` TCP 80 (plus `fd00:ec2::23/128` if the cluster is dual-stack); ingress from `factory-sandbox` and from `monitoring` (metrics);
  - smokescreen → 443/80 to `0.0.0.0/0` (Smokescreen itself enforces the allowlist and private-IP denial); ingress from `factory-sandbox`;
  - CNPG pods, selected by `cnpg.io/cluster In (factory-db, factory-db-seed)` so the `full-recovery` and `initdb` Job pods are covered as well as instances → 443 to `0.0.0.0/0` (S3, kube-apiserver) and the Pod Identity agent `169.254.170.23/32` TCP 80; ingress 5432 from gateway, 8000/5432 from `cnpg-system`, 9187 from `monitoring`. With `instances: 1` no instance-to-instance rule is needed; a later increase must add 5432/8000 between CNPG pods.

**Step 6 — Runner image (smoke subset)** (`runner/Dockerfile`, `runner/egress.Dockerfile`)
- Runner: Node LTS + `@anthropic-ai/claude-agent-sdk` pinned, Go pinned, `curl`; non-root; works with read-only root FS given `HOME=/home/runner` and `/tmp` on `emptyDir`s.
- `runner/smoke/smoke.mjs`: one Agent SDK query ("reply with OK"); non-zero exit unless the result is OK.
- `runner/probe/probe.sh`: waits 5 s (VPC CNI applies policy shortly after pod start), then checks and exits non-zero on any violation: no env var, and no file in the mounted volumes, `$HOME` or `/tmp`, matches `sk-ant` (the image layers are covered by the CI build, which never sees the key); no `AWS_*` / Pod Identity env; `curl 169.254.169.254` fails; `curl` to `factory-db-rw.factory:5432` fails; direct `curl https://api.anthropic.com` fails; via proxy `https://proxy.golang.org` succeeds and `https://example.com` fails; `http://gateway.factory:8080/v1/models` with the token succeeds.
- Egress image: Smokescreen at a pinned commit, distroless, non-root.

**Step 7 — Sandbox Jobs** (`deploy/sandbox/job.yaml.tmpl`, applied by `make smoke` / `make sandbox-probe`, not by Argo)
- One pod spec shared by both targets: ns `factory-sandbox`, SA `sandbox` (no Pod Identity association, `automountServiceAccountToken: false`), toleration + nodeSelector for the `sandbox` pool, `activeDeadlineSeconds: 300`, PSA-restricted securityContext.
- The per-run Secret holds exactly `ANTHROPIC_AUTH_TOKEN=<minted token>`. Env also sets `ANTHROPIC_BASE_URL=http://gateway.factory.svc.cluster.local:8080`, `HTTPS_PROXY`/`HTTP_PROXY=http://smokescreen.factory.svc.cluster.local:4750`, `NO_PROXY=gateway.factory,gateway.factory.svc,gateway.factory.svc.cluster.local,.svc.cluster.local`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, `DISABLE_TELEMETRY=1`, `DISABLE_AUTOUPDATER=1`.
- `make smoke` mints a token for run `phase0-smoke-<ts>`, runs the smoke command, waits for completion, prints `agent_calls` rows for that run.
- `make sandbox-probe` runs `probe.sh` with the same spec and prints its report. `make sandbox-probe CONTROL=1` runs the same pod spec in a throwaway namespace `factory-probe-control-<ts>` that has no NetworkPolicies (created with the PSA `restricted` label and deleted by the target); there the probe is expected to exit non-zero. A pod selected by any egress policy stays isolated, and Argo self-heal would restore a deleted policy, so deleting a policy would prove nothing; this control does.

**Step 8 — First boot and helpers** (`Makefile`, `scripts/`)
- `make db-init` (`scripts/factory-db-init.sh`, once per account): refuses if `s3://<bucket>/cnpg/factory-db/` is non-empty (with `--purge-failed` to clear a prefix that has no completed base backup); applies Cluster `factory-db-seed` (`initdb` with `database: factory`, `owner: factory`, so the chart's `bootstrap.recovery.database/owner` finds them in the backup; same ObjectStore; plugin `serverName: factory-db`); once the seed is healthy, creates a `Backup` (`method: plugin`, `pluginConfiguration.name: barman-cloud.cloudnative-pg.io`) and waits for `completed`; deletes Argo's stuck `factory-db` Cluster first and then the seed, so self-heal recreates `factory-db` and its recovery finds the backup; waits for `factory-db` healthy.
- `make factory-status` (`scripts/factory-status.sh`; named so it does not shadow platform `make status`): if the prefix is empty, prints "first bring-up: run make db-init" and exits 0; otherwise waits for `factory-db` healthy and the gateway Ready, prints `cnpg_pg_stat_archiver_failed_count` and the last backup time, and exits non-zero if the failed count is above 0 or the gateway is not Ready.
- `make verify-row ID=<id>`: prints the row via `kubectl exec` psql on `factory-db-rw`.
- Bring-up is platform `make up` then `make -C ~/repos/dark-factory/dark-factory factory-status`; tear-down is platform `make down` (G1 does the backup). Argo retries (`retry.limit: 10`, about 25 min) cover the factory app syncing before plugin CRDs exist; `factory-status` reports if it is still failing.

## 4. Acceptance criteria

- AC1 `go test ./...` and `golangci-lint run` pass in `orchestrator/`.
- AC2 Gateway tests against an `httptest` fake upstream replaying Step 0b fixtures, with real Postgres (container): (a) non-stream → one row with exact tokens; (b) stream → client receives identical bytes, row has input/cache tokens from `message_start` and output tokens equal to the **last** `message_delta` value; (c) no/invalid/expired token → 401 and zero upstream requests; (d) token + mismatching `X-Factory-Run` → 403; (e) inbound `x-api-key`, `authorization`, `accept-encoding` never reach upstream; (f) gzip-capable client still gets correct usage; (g) client cancel mid-stream → row with `http_status = 499`; (h) `count_tokens` passes through and writes no row; (i) captured logs never contain the configured key; (j) two gateways starting at once both come up and migrations ran once.
- AC3 `terraform validate`, `terraform test` (incl. 2a assertions), `scripts/validate-gitops.sh` and `make lint` pass in platform-project.
- AC4 `make sandbox-probe` exits 0 on the real cluster, and `make sandbox-probe CONTROL=1` exits non-zero (the same pod spec without policies breaks the isolation checks, so enforcement is shown to be live).
- AC5 An `ExternalSecret` for `${name}/factory/anthropic-api-key` created in another namespace reports `SecretSyncedError` (AccessDenied).
- AC6 `make smoke` exits 0 and prints ≥ 1 `agent_calls` row for its run id; every row has non-null cost; the main-model row has input and output tokens > 0.
- AC7 **Exit criterion:** note a row id from AC6 → platform `make down` (output shows the G1 backup completed) → platform `make up` → `make factory-status` → `make verify-row ID=<id>` returns the identical row. Commands and output, including the `factory-status` archiver count and last backup time before and after, are recorded in `docs/demos/phase0-exit.md`.
- AC8 `aws s3api get-bucket-versioning` = `Enabled`; `aws s3api get-bucket-lifecycle-configuration` shows no current-object expiration; after AC7, `kubectl get backups -n factory` lists the G1 backup as `completed`.

## 5. Risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| R1 | First bring-up: recovery Cluster has nothing to recover | `make factory-status` detects the empty prefix and says to run `make db-init`; `db-init` sequence in Step 8 |
| R2 | Two writers on `serverName: factory-db` (skip-check on) | Only two Cluster objects ever write it: the seed and the chart's. `db-init` refuses on a non-empty prefix. While the seed runs, Argo's stuck `factory-db` keeps retrying recovery and could succeed in the seconds between the seed's backup completing and `db-init` deleting it. That brief overlap is harmless: WAL file names carry the timeline, so the two do not overwrite each other, and the seed holds no user data. `db-init` deletes Argo's Cluster first to keep the window short |
| R3 | Last WAL not archived before PVC deletion | G1 forces `pg_switch_wal()` and waits for the archiver before teardown proceeds; aborts teardown on failure |
| R4 | Key reachable by another workload | E1: no Secret object; ESO `Deny`; AC5 tests it; sandbox pods have no Pod Identity and IMDS hop limit 1 |
| R5 | Agent SDK traffic bypasses the gateway | Enforced NetworkPolicy (2c) + Smokescreen allowlist excludes Anthropic; AC4 probes direct access |
| R6 | NetworkPolicy enforcement change disturbs existing platform workloads | No existing namespace declares policies, so VPC CNI leaves them open; rehearse on platform `make up` before factory deploy |
| R7 | Plugin incompatible (skip-check, PSA, boto3) | Step 0a before building; named fallbacks B2 / B-ii |
| R8 | Spot capacity for `sandbox` | Several instance families in `extraPools`; Jobs re-runnable |
| R9 | CNPG app password mismatch after recovery | `bootstrap.recovery.database/owner` set so the operator reconciles the `factory-db-app` Secret; AC7 fails loudly if the gateway cannot connect |

## 6. Pre-mortem (deliberate mode)

1. **"The row vanished after `make up`."** The insert's WAL was never archived, or base backups were gone. Prevention: G1 archive wait; no S3 current-object expiry and Barman retention always keeps the newest backup; one rehearsal of down/up before the recorded demo. Detection: AC7, AC8.
2. **"Isolation was never enforced."** NetworkPolicy silently ignored, or the key obtainable through ESO. Prevention: 2c, E1, ESO `Deny`. Detection: AC4 including the negative control (policy removed → probe fails), AC5.
3. **"Phase 0 took three weekends."** Plugin surprises and cross-repo churn. Prevention: Step 0 spike first with explicit fallbacks; one platform-project PR; scope trimmed to the Anthropic secret only; if the plugin path is still blocked after half a day, switch to B2 and record the debt in the ADR. If Saturday slips, cut in this order: `extraPools` becomes a second hard-coded NodePool; CI image builds become a local push like platform `scripts/backstage-image.sh`; the ServiceMonitor is deferred.

## 7. Expanded test plan (deliberate mode)

- **Unit:** `internal/usage` SSE parser (split frames, `ping`, `error` event, last-value `message_delta`, server-tool usage); pricing per model and unknown model; token hashing/expiry; header/token mismatch.
- **Integration:** AC2 (a)–(j) with real Postgres in a container and the fake upstream; goose up on empty DB; `terraform test` for 2a.
- **E2E (real cluster):** AC4 (+ negative control), AC5, AC6, AC7, AC8.
- **Observability:** `make factory-status` prints the archiver failed count (non-zero fails the target) and the last backup time; gateway ServiceMonitor target is `up` in Prometheus; `gateway_requests_total` increments during `make smoke`. Checked in verification step 3.

## 8. Verification steps

1. `cd orchestrator && go test ./... && golangci-lint run` (AC1, AC2).
2. platform-project: `make lint test` (AC3).
3. platform `make up`; `make factory-status` → `make db-init` → `make factory-status`; `make sandbox-probe` and `make sandbox-probe CONTROL=1` (AC4); AC5 ExternalSecret check; `make smoke` (AC6); Prometheus target and counter check.
4. platform `make down`; platform `make up`; `make factory-status`; `make verify-row ID=<id>` (AC7); AC8 commands. Paste output into `docs/demos/phase0-exit.md`.

## 9. ADR

- **Decision:** Phase 0 delivers a key-holding Go gateway with IAM-isolated key access, a CNPG cluster backed up through the Barman Cloud plugin to a baseline-layer bucket with always-recovery bring-up, Smokescreen egress, an enforced-NetworkPolicy sandbox namespace on a tainted spot pool, and a generic pre-teardown backup in platform-project.
- **Drivers:** time to exit criterion; restore after any downtime and on first boot; demonstrable isolation.
- **Alternatives considered:** A2 own AppProject; B2 in-tree backup; B-ii per-incarnation `serverName`; C2 Squid, C3 FQDN policy, C4 in-house proxy; D2 buffered proxy; E2 namespaced SecretStore, E3 ClusterSecretStore; F PreSync/Sync-wave hooks; G2 factory-side `make down` wrapper.
- **Why chosen:** each choice is the one that keeps the exit criterion reachable in one weekend without weakening key isolation or durability; where a platform change was unavoidable it was made generic.
- **Consequences:** platform-project gains `extraPools`, NetworkPolicy enforcement, a CNPG-aware teardown step and the Barman plugin; the gateway departs from the ESO pattern; B-i relies on one-writer discipline; images exist only after merges to `main`.
- **Follow-ups:** Phase 1 adds `runs` FK on `agent_calls`, the orchestrator-internal `X-Factory-*` path, the running-stage check in teardown (§7.4), and GitHub/Discord secrets; Phase 3 fills the `Budget` hook; revisit B-ii if two-writer risk ever materialises.

## 10. Changelog

- v2: enabled VPC CNI NetworkPolicy and completed the policy set (Architect 1, Critic M2); migrations at gateway startup with advisory lock, PreSync hook removed (A2, M1); seed Cluster `factory-db-seed` with its own SA binding and explicit first-boot sequence (A3, C1); removed S3 base-backup expiry and `prevent_destroy`, added Barman `retentionPolicy` and `force_destroy` var (A4/A5, C2, minor 5/6); key read directly by gateway via Pod Identity with ESO `Deny`, new AC5 (A6, M3); bucket name deterministic in chart values, dead annotation removed (A9, M4); `make sandbox-probe` with negative control replaces exec-based AC4 (M5); usage last-value merge, `Accept-Encoding` stripping, `/v1/messages`-only rows, cancel-safe write (A7, M6, minor 1); pre-teardown backup moved into platform teardown after wake (A8, minor 8); Squid → Smokescreen (Architect antithesis C1, minor 10); separate `factory-sandbox` namespace (minor 9); generic `extraPools` (Architect synthesis); Step 0 spikes for plugin unknowns (A10, open questions); KMS policy delegation (minor 7); nested registration file (minor 14); mint-token location (minor 15); Phase 1 scope trimmed (A11, minor 3); token authoritative over headers (minor 2); `make verify-row`/`status` defined (minor 12); observability checks in verification (minor 13); pre-mortem 2 replaced with isolation scenario.
- v3 (folded in without another review round, as the Critic allowed; required changes from the Architect's v2 review and the Critic's APPROVE): Pod Identity agent egress for the gateway and CNPG pods, and CNPG policies selected by `cnpg.io/cluster` so the recovery and initdb Jobs are covered; `instances: 1`; bucket renamed to the platform convention, with a name test; `s3:ListBucket` with a prefix condition; Smokescreen no-mTLS `default` ACL and deny-range check; seed `initdb` database and owner, an explicit seed `Backup`, Argo's Cluster deleted before the seed; R2 states the brief overlap honestly; G1 waits for health, skips uninitialised clusters with a warning, is bounded by `TEARDOWN_TIMEOUT_BACKUP`, passes `FORCE` through, warns on early-exit paths, execs with `-c postgres`, and has teardown tests; AC4's negative control moved to a throwaway policy-free namespace; gateway startup retries with backoff; the optional platform secret list; a narrower probe scan; `make factory-status` (fails on a non-zero archiver count); a cut order for a slipping weekend; a 2c rollback note; the demo record includes backup evidence.
- v4 (team-verify findings, 2026-10-18): the gateway forwards only an allowlist of paths, with per-token concurrency, body and connection limits, a TTL cap and `revoke-token`; the chart derives platform name, region, account and bucket from `image.repository`, because the services ApplicationSet passes only three parameters; the factory gets its own AppProject and protected namespaces (2h); teardown 1b reads "initialised" from the ObjectStore and fails closed when listing fails; `s3:ListBucket` has no prefix condition; sandbox Jobs get an init container that waits for policy enforcement; an empty optional secret produces a preflight warning, not a failure. Accepted for Phase 0 and to be fixed before Phase 1: server tools and MCP connectors get around the egress allowlist, DNS can be used to exfiltrate from the sandbox, and gateway egress is in-VPC 443 (ADR 0001).
