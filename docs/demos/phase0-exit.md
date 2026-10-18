# Phase 0 exit demo

This file records AC4 and AC6 to AC8 from `.omc/plans/ralplan-phase0-foundations.md` as shown on the real cluster, plus the Step 0b fixture capture that AC2 sign-off needs (section 6). Replace each `<paste …>` with the command's real output, unedited apart from redacting account IDs if you want to.

**Exit criterion (SPEC.md §9):** a sandbox Job calls Claude through the gateway with no key in the pod, the call appears in `agent_calls`, and the row is still there after `make down` then `make up`.

- Date: `<YYYY-MM-DD>`
- dark-factory commit: `<sha>`
- platform-project commit: `<sha>`
- Kube context: `export FACTORY_KUBE_CONTEXT=<context>`

## 1. Bring-up

```console
$ make -C ~/repos/platform-project up
<paste the tail of the output>

$ make factory-status
<paste; on the very first bring-up this says "first bring-up: run make db-init">

$ make db-init          # first bring-up only
<paste>

$ make factory-status
<paste: factory-db Ready, gateway Ready, archiver failed count, last backup time>
```

## 2. Isolation (AC4)

AC4 passes when `make sandbox-probe` exits 0 and `CONTROL=1 scripts/sandbox-probe.sh` exits 1.

```console
$ make sandbox-probe; echo "exit=$?"
<paste the PASS/FAIL report, with the wait-for-policy line "gateway reachable and direct egress to 1.1.1.1 dropped 3 times in a row"; expect exit=0>

$ CONTROL=1 scripts/sandbox-probe.sh; echo "exit=$?"
<paste; expect the "probe: pod" header, FAIL direct-anthropic among others, "control: the probe failed as expected", and exit=1>
```

The control runs the script directly because `make` turns every non-zero exit into 2. It exits 1 only when the probe ran and reported `FAIL direct-anthropic`. exit=3 ("inconclusive") means the Job failed for another reason (image pull, scheduling, the probe never ran, or only other checks failed) and does not count for AC4: fix the cause and run it again. exit=0 means the probe passed without NetworkPolicies, which also fails AC4.

## 3. Smoke call through the gateway (AC6)

```console
$ make smoke; echo "exit=$?"
<paste: the Job log ending "smoke: OK", the agent_calls rows, and exit=0>
```

Row id noted for AC7: `ID=<id>`

```console
$ make verify-row ID=<id>
<paste the row before teardown>
```

Prometheus check, from section 7 of the plan:

- Target `factory/gateway` is `up`: `<yes/no>`
- `gateway_requests_total` went up during `make smoke`: `<before> → <after>`

## 4. Down and up (AC7)

```console
$ make factory-status
<paste: archiver failed count and last backup time before teardown>

$ make -C ~/repos/platform-project down
<paste the pre-teardown backup step (1b), showing the Backup completed and the WAL archived>

$ make -C ~/repos/platform-project up
<paste the tail>

$ make factory-status
<paste: archiver failed count and last backup time after bring-up>

$ make verify-row ID=<id>
<paste; must be identical to the row before teardown>
```

Before and after are identical: `<yes/no>`

## 5. Bucket and backups (AC8)

```console
$ aws s3api get-bucket-versioning --bucket <bucket>
<paste; expect "Status": "Enabled">

$ aws s3api get-bucket-lifecycle-configuration --bucket <bucket>
<paste; expect only a NoncurrentVersionExpiration rule, with no current-object Expiration>

$ kubectl --context "$FACTORY_KUBE_CONTEXT" -n factory get backups
<paste; the pre-teardown backup from step 1b is listed as completed>
```

## 6. Real usage fixtures (Step 0b, AC2)

AC2 is not signed off while `orchestrator/internal/usage/testdata/` holds the synthetic fixtures. From your own shell, with the key only in the environment:

- [ ] Capture `message.json`, `stream.sse` and `stream_server_tool.sse` with the commands in `orchestrator/internal/usage/testdata/README.md`. Keep `stream_error.sse` synthetic.
- [ ] Review each file: no key, no personal content.
- [ ] Update the token expectations in the usage and gateway tests in the same commit, and say in the commit message that the spec changed (real data replaced synthetic), per CLAUDE.md.
- [ ] `make -C orchestrator test lint` passes.

Fixture commit: `<sha>`

## Open questions to answer on the live cluster

- How long after `make up` does the Barman plugin populate `ObjectStore.status.serverRecoveryWindow`? Teardown step 1b reads it. `<seconds, or "not populated" and what was done>`
- Does Argo CD match the `factory` AppProject's `sourceRepos` against the registration's `repoURL` with the right case (`LinuxAd`)? `<yes/no; evidence: svc-factory synced, or the error>`

## Notes

`<anything that went wrong, and what was changed>`
