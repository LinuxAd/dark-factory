# Dark Factory

The factory turns an idea written as a GitHub issue into a GitHub repo whose code passes hidden acceptance scenarios, with no human writing code. `SPEC.md` is the specification and `CLAUDE.md` holds the rules for building it.

## Phase 0: foundations

Phase 0 builds the platform pieces underneath the factory, and no pipeline stage yet:

- a model gateway that holds the only copy of the Anthropic key and records every call in `agent_calls`;
- a CloudNativePG Postgres database backed up to S3;
- a Smokescreen egress proxy;
- a `factory-sandbox` namespace whose Jobs reach only the proxy and the gateway.

**Exit criterion:** a sandbox Job calls Claude through the gateway with no key in the pod, the call appears in `agent_calls`, and the row is still there after `make down` then `make up`.

The decisions behind this are in [ADR 0001](docs/adr/0001-phase0-foundations.md). The recorded demo goes in [docs/demos/phase0-exit.md](docs/demos/phase0-exit.md).

## Running it

Everything runs on the `platform-project` cluster. Argo CD deploys `deploy/chart` from `gitops/services/factory.yaml` there. Every target that touches the cluster needs its kubectl context set explicitly:

```sh
export FACTORY_KUBE_CONTEXT=<platform cluster context>
```

Bring-up:

```sh
make -C ~/repos/platform-project up
make factory-status
```

**First boot, once per account.** On the very first bring-up the backup bucket is empty, so the database has nothing to recover from, and `make factory-status` prints `first bring-up: run make db-init`. Run:

```sh
make db-init           # PURGE_FAILED=1 clears a failed earlier attempt
make factory-status
```

Teardown, which backs the database up first:

```sh
make -C ~/repos/platform-project down
```

Checks:

```sh
make sandbox-probe             # isolation checks; exits 0
make sandbox-probe CONTROL=1   # same pod, no NetworkPolicies; must fail
make smoke                     # one Agent SDK call, prints its agent_calls rows
make verify-row ID=<id>        # one agent_calls row
```

## Development

```sh
make -C orchestrator lint test   # Go, in Docker
make lint                        # shellcheck, helm lint, kubeconform
make images                      # build the runner and egress images locally
```

CI (`.github/workflows/ci.yml`) runs the same checks on every pull request. On `main` it also pushes the gateway, runner and egress images, tagged with the commit SHA.
