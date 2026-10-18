# 0001: Phase 0 foundations

Status: Accepted, 2026-10-18

## Context

The Phase 0 exit criterion (SPEC.md §9) is: a sandbox Job calls Claude through the gateway with no key in the pod, the call appears in `agent_calls`, and the row is still there after `make down` then `make up`.

The factory runs on the existing `platform-project` cluster. That cluster has these properties:

- Argo CD deploys service repos through the `services` ApplicationSet. That ApplicationSet injects only `image.repository`, `image.tag` and `route.host`.
- The `services` AppProject allows only one cluster-scoped kind, `Namespace`.
- VPC CNI did not enforce NetworkPolicy until this phase.
- `make down` deletes every PVC.
- Sleep deletes the NodePools.

The full plan, with the options weighed for each decision, is `.omc/plans/ralplan-phase0-foundations.md`. This record states what was chosen and why.

## Decision

**A. Where cluster-scoped and IAM pieces live.** platform-project owns everything that is cluster-scoped, IAM or CNI:

- the S3 bucket and its KMS key, plus the Secrets Manager container (`terraform/01-baseline/factory.tf`);
- the Pod Identity roles for SAs `factory-db`, `factory-db-seed` and `gateway`;
- the explicit External Secrets `Deny` on `${name}/factory/*`;
- `enableNetworkPolicy` on VPC CNI;
- a generic `extraPools` list in the `karpenter-nodes` chart. It defines the `sandbox` pool: spot, label `platform/nodegroup: sandbox`, taint `platform/sandbox=true:NoSchedule`;
- the Barman Cloud plugin add-on;
- the registration file `gitops/services/factory.yaml`.

Each of these is made generic. This repo's `deploy/chart` is namespaced only, apart from its two `Namespace` objects. Rejected: a factory AppProject allowed cluster-scoped kinds. It would put the NodePool in the `workload` teardown tier and widen what a non-platform repo can create.

**B. Postgres backup and bring-up.**

- **B1: the backup mechanism.** CloudNativePG 1.30.1 with the Barman Cloud plugin v0.15.1 (Helm chart 0.8.1, in `cnpg-system`). Backups go to an `ObjectStore` at `s3://<bucket>/cnpg` with `retentionPolicy: "30d"`, credentials `inheritFromIAMRole`, and `AWS_REGION` set on the sidecar. A `ScheduledBackup` runs nightly. Rejected: the in-tree `barmanObjectStore`, which has been deprecated since 1.26.
- **B-i: bring-up.** The chart's Cluster `factory-db` always bootstraps with `bootstrap.recovery` (database `factory`, owner `factory`), with a fixed plugin `serverName: factory-db` and the annotation `cnpg.io/skipEmptyWalArchiveCheck: "enabled"`. The very first bring-up is `make db-init`:
  1. It refuses unless the prefix is empty.
  2. It applies the seed Cluster `factory-db-seed`, which runs `initdb` with the same database, owner, store and serverName.
  3. It takes a plugin `Backup` of the seed.
  4. It deletes Argo CD's stuck `factory-db`, then the seed.
  5. Self-heal recreates `factory-db`, which recovers from that backup.

  Rejected: a new `serverName` per bring-up (B-ii). It needs either a factory-specific value in the shared ApplicationSet or a git commit on every `make up`.

**C. Egress proxy.** Smokescreen v0.1.0 (commit `609eb89`), built in `runner/egress.Dockerfile`: `golang:1.26.9`, then distroless static, nonroot. Sandbox clients present no certificate, so the config file sets `allow_missing_role: true`. This is a YAML key; there is no flag for it. The ACL's `default` rule is `enforce` with the SPEC.md §7.2 allowlist. Smokescreen's built-in deny list already covers 10/8, 172.16/12, 192.168/16, 169.254/16, 100.64/10 and loopback, so no `--deny-range` is needed. Rejected:

- Squid: root images, and no private-IP blocking by default.
- FQDN NetworkPolicy: VPC CNI has no FQDN rules.
- An in-house proxy: we would be writing a security boundary instead of reusing one.

**D. Usage capture.** `httputil.ReverseProxy`, with a tee on the response body that feeds an SSE/JSON usage parser. Rows are written only for `POST /v1/messages`. The last `message_delta.usage` overwrites earlier values; values are never summed. Outbound `Accept-Encoding` is stripped. The row write survives a client cancel and is recorded with status 499. Rejected: buffering the response, which breaks streaming.

**E. Key delivery.** The gateway reads `${name}/factory/anthropic-api-key` at startup with the AWS SDK, through its own Pod Identity role. The key is never in a Kubernetes Secret, so it is never in etcd. The shared External Secrets role has an explicit `Deny` on `${name}/factory/*`. Rejected:

- A namespaced SecretStore: the key would still land in etcd.
- The existing ClusterSecretStore: any services namespace could read the key.

**F. Migrations.** `goose`, with SQL embedded from `memory/migrations/`. Migrations run at gateway startup under `pg_advisory_lock`, and readiness fails until they finish. Rejected: an Argo PreSync hook, which deadlocks before the Cluster exists.

**G. Pre-teardown backup.** This is a generic step "1b" in platform-project `scripts/teardown.sh`. It covers every CNPG Cluster labelled `platform/backup-before-teardown: "true"`:

1. It waits for the Cluster to be healthy.
2. It takes an on-demand plugin `Backup` and waits for it to complete.
3. It runs `pg_switch_wal()` and waits for the archiver to reach that WAL.

A Cluster that is healthy but fails its backup aborts teardown, and teardown prints `FORCE=yes make down`. A Cluster that never became healthy and has no backup (uninitialised) is skipped with a warning. Rejected: a `make down` wrapper in this repo. It would be skipped whenever teardown is run from platform-project.

**Network.** Both namespaces carry Pod Security `restricted`. Both have default-deny ingress and egress, plus DNS to kube-dns. The allowed flows are:

| From | To |
|---|---|
| `factory-sandbox` pods | `smokescreen:4750` and `gateway:8080` only |
| gateway | Postgres 5432; 443 anywhere; the Pod Identity agent `169.254.170.23/32:80`. Ingress from `factory-sandbox`, and from `monitoring` on 9090 |
| Smokescreen | 443 and 80 anywhere; Smokescreen enforces the allowlist itself. Ingress from `factory-sandbox` |
| CNPG pods (`cnpg.io/cluster In (factory-db, factory-db-seed)`, which also covers the recovery and initdb Job pods) | 443 anywhere; the Pod Identity agent. Ingress 5432 from the gateway, 8000/5432 from `cnpg-system`, 9187 from `monitoring` |

## Step 0a outcomes

Recorded in [docs/research/phase0-spikes.md](../research/phase0-spikes.md). Every question came back "yes", so no fallback was needed:

- The plugin honours `cnpg.io/skipEmptyWalArchiveCheck: "enabled"` for WAL archiving and for the recovery Job.
- The plugin sidecar meets Pod Security `restricted`.
- botocore 1.43.105 supports Pod Identity.
- Recovery with `database`/`owner` reconciles the `factory-db-app` Secret. The database and owner must already exist in the backup, so the seed uses `factory`/`factory`.
- Smokescreen's default deny ranges cover the VPC and pod CIDRs.
- The Pod Identity agent is at `169.254.170.23` (ports 80 and 2703; IPv6 `fd00:ec2::23`). Its traffic is assumed to be subject to policy, so it is allowed explicitly.

Still to check on the live cluster:

- the sidecar receives Pod Identity credentials;
- default-deny blocks the Pod Identity agent until the allow rule is added;
- one admission test under `restricted`.

## Consequences

- platform-project gains these features: `extraPools`, NetworkPolicy enforcement, a CNPG-aware teardown step and the Barman plugin. Phase 0 needs a cross-repo PR, which Adam merges before the chart is deployed.
- The gateway departs from the External Secrets pattern, and rotating the key needs a gateway restart.
- B-i turns off the two-writer guard. It is safe only because exactly two Cluster objects ever write `serverName: factory-db`, and `db-init` refuses on a non-empty prefix. During `db-init`, Argo CD's Cluster may briefly overlap the seed. That overlap is harmless: WAL names carry the timeline, and the seed holds no user data.
- Images exist only after a merge to `main`, because CI pushes them. The first bring-up depends on that merge.
- Raising `instances` above 1 needs a NetworkPolicy rule for 5432/8000 between CNPG pods.

## Follow-ups

- Phase 1: add the `runs` FK on `agent_calls`, the orchestrator-internal `X-Factory-*` caller path, the running-stage check in teardown (SPEC.md §7.4), and the GitHub and Discord secrets.
- Phase 3: fill in the `Budget` hook.
- Revisit B-ii if the two-writer risk ever becomes real.
