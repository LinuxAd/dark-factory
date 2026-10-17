# Phase 0 spikes (Step 0a)

Researched 2026-10-17 from primary sources:
- plugin-barman-cloud v0.15.1 (6216b7d)
- cloudnative-pg v1.30.1 (2a35abb)
- cloudnative-pg/charts main (25d711a)
- stripe/smokescreen v0.1.0 (609eb89)

Every Step 0a question came back "yes", so the plan's chosen options hold and no fallback is needed. Some points can only be checked on the real cluster; they are under "Live checks still needed" at the end.

| # | Question | Verdict | Plan impact |
|---|---|---|---|
| 1 | Versions | Chart `cloudnative-pg` 0.29.1 ships operator **1.30.1**. Plugin **v0.15.1** (2026-09-30), Helm chart `plugin-barman-cloud` **0.8.1** from `https://cloudnative-pg.github.io/charts`. Needs CNPG ≥ 1.26 and cert-manager, and must run in `cnpg-system`. The Helm Deployment is named `plugin-barman-cloud`; the manifest names it `barman-cloud` | B1 holds; pin chart 0.8.1 |
| 2 | `cnpg.io/skipEmptyWalArchiveCheck` | **Yes, it is honoured.** The value must be exactly `"enabled"`. CNPG makes the empty-archive decision and the plugin obeys it, for WAL archiving (`archiver.go` in CNPG, `internal/cnpgi/common/wal.go` in the plugin) and for the recovery Job (`restore_job.go`). The plugin has no parameter of its own for this | B-i holds; B-ii not needed |
| 3 | Pod Security `restricted` for the sidecar | **Yes.** `reconcilePodSpec` sets non-root, no privilege escalation, drop ALL, RuntimeDefault seccomp and a read-only root filesystem. The image is distroless and runs as `USER 26:26` | None |
| 4 | Pod Identity and the ObjectStore | botocore **1.43.105**, well above the 1.34.41 minimum. Auth is `s3Credentials.inheritFromIAMRole: true`. Pod Identity env vars are copied from the postgres container into the sidecar, but upstream has not tested Pod Identity | Holds; live-check it |
| 5 | Recovery with `database`/`owner` set | **Yes.** CNPG creates the `<cluster>-app` Secret and sets the owner's password to match it once recovery reaches primary. The database and owner must already exist in the backup | Seed `initdb` must use `factory`/`factory` |
| 6 | Smokescreen | Pin **v0.1.0 (609eb89)**; prebuilt tarballs with SHA256SUMS exist, and it builds with Go ≥ 1.25. Details below | C1 holds; wording fixed below |
| 7 | Pod Identity agent | `169.254.170.23` on ports 80 and 2703; IPv6 `fd00:ec2::23`. The agent is installed (`terraform/02-platform/eks.tf:25`). No primary source says whether VPC CNI policy applies to this traffic, so assume it does | Explicit egress allow, as the plan already says |

## Smokescreen details

There is **no command-line flag** for missing client roles. Set it as a YAML key in the file passed with `--config-file`:

```yaml
allow_missing_role: true
```

With that set, a client that presents no certificate gets the ACL's `default` rule. If the ACL has no `default`, every request is denied.

ACL file (`--egress-acl-file`):

```yaml
version: v1
services: []          # must be present
default:
  name: default
  project: factory
  action: enforce
  allowed_domains: [proxy.golang.org, sum.golang.org, registry.npmjs.org, github.com, api.github.com, "*.githubusercontent.com"]
```

Wildcards and matching:
- A leading `*.` matches one or more subdomain labels.
- It does not match the bare apex domain.
- Only one `*` is allowed per pattern.

Network defaults:
- Default listen port is 4750.
- Denied by default: 10/8, 172.16/12, 192.168/16, 169.254/16, 100.64/10, loopback, and the proxy's own IPs. So `--deny-range` is not needed.
- CONNECT targets are resolved, and the address actually dialled is the one checked against the deny list.

Metrics: `--expose-prometheus-metrics` serves them on port 9810.

Upstream has no Dockerfile at the v0.1.0 tag. Write our own:
- Build stage: `golang:1.26`, running `CGO_ENABLED=0 go build`.
- Runtime stage: `gcr.io/distroless/static:nonroot`.

## Plugin YAML shape

This is the shape for namespace `factory`.

Rules:
- `ObjectStore.spec.configuration.serverName` must **not** be set; a CEL rule rejects it. Set `serverName` as a plugin parameter on the Cluster instead.
- `externalClusters` is required for recovery, even though it points at the same store.
- `plugins` is required, or the recovered cluster does not archive.
- Cron schedules have six fields, with seconds first.

```yaml
apiVersion: barmancloud.cnpg.io/v1
kind: ObjectStore
metadata: { name: factory-db-store, namespace: factory }
spec:
  retentionPolicy: "30d"
  configuration:
    destinationPath: s3://<bucket>/cnpg
    s3Credentials: { inheritFromIAMRole: true }
    wal: { compression: gzip, maxParallel: 2 }
  instanceSidecarConfiguration:
    env: [{ name: AWS_REGION, value: <region> }]   # Pod Identity injects no region
---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: factory-db
  namespace: factory
  annotations: { cnpg.io/skipEmptyWalArchiveCheck: "enabled" }
spec:
  instances: 1
  bootstrap:
    recovery: { source: origin, database: factory, owner: factory }
  plugins:
    - name: barman-cloud.cloudnative-pg.io
      isWALArchiver: true
      parameters: { barmanObjectName: factory-db-store, serverName: factory-db }
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cloudnative-pg.io
        parameters: { barmanObjectName: factory-db-store, serverName: factory-db }
---
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata: { name: factory-db-nightly, namespace: factory }
spec:
  schedule: "0 0 2 * * *"
  backupOwnerReference: self
  cluster: { name: factory-db }
  method: plugin
  pluginConfiguration: { name: barman-cloud.cloudnative-pg.io }
```

An on-demand `Backup` has the same `cluster`, `method` and `pluginConfiguration` fields.

## Live checks still needed (Adam runs these on the cluster)

1. The sidecar receives Pod Identity credentials, and S3 calls work with `AWS_REGION` set.
2. A default-deny egress policy blocks the Pod Identity agent until the explicit allow is added.
3. One admission test with Pod Security `restricted` on the namespace.
