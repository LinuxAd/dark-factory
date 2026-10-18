# Dark Factory

## Why "dark factory"?

A "dark factory", also called "lights-out manufacturing", is a plant automated enough to run without people on site, so the lights can be switched off. The best-known example is FANUC in Japan, whose robots build other robots unattended for long stretches.

The term carries over to software. A dark software factory turns specifications into working code with no human writing the code. Humans write the spec and review or merge the result, and the factory does the rest.

This repo applies that model to one pipeline. A human writes an idea as a GitHub issue and signs off at three gates. In between, the factory writes all the code, and the result must pass hidden acceptance scenarios that the implementing agents never see.

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
make sandbox-probe CONTROL=1   # same pod, no NetworkPolicies; see below
make smoke                     # one Agent SDK call, prints its agent_calls rows
make verify-row ID=<id>        # one agent_calls row
```

`make sandbox-probe CONTROL=1` is the negative control. It runs the probe without NetworkPolicies and without the Job's wait-for-policy init container. It exits 1, the expected result, only when the probe ran and reported `FAIL direct-anthropic`. It exits 3 and prints "inconclusive" when the Job failed for any other reason: the image did not pull, the pod was not scheduled, the probe never ran, or only other checks failed. It exits 0 if the probe passed, which means the control showed nothing. `make` reports every non-zero exit as 2, so run `CONTROL=1 scripts/sandbox-probe.sh` directly to see the 1 or 3; the last log line also says which.

`make smoke` and `make sandbox-probe` mint a 1h gateway token for their run and revoke it on exit with `gateway revoke-token --run`. A failed revoke is logged and does not change the exit status; the token then expires at its TTL.

## Development

```sh
make -C orchestrator lint test   # Go, in Docker
make lint                        # shellcheck, helm lint, kubeconform
make images                      # build the runner and egress images locally
```

CI (`.github/workflows/ci.yml`) runs the same checks on every pull request. On `main` it also pushes the gateway, runner and egress images, tagged with the commit SHA.

Pushing on `main` needs four repository variables. A repo created from the platform's service template gets them automatically; this one did not, so set them once from platform-project's baseline outputs:

```sh
cd ~/repos/platform-project
gh variable set AWS_ROLE_ARN   -R LinuxAd/dark-factory --body "$(scripts/tf.sh baseline output -raw ci_role_arn)"
gh variable set AWS_REGION     -R LinuxAd/dark-factory --body <region>
gh variable set ECR_REGISTRY   -R LinuxAd/dark-factory --body "$(scripts/tf.sh baseline output -raw ecr_registry)"
gh variable set ECR_REPOSITORY -R LinuxAd/dark-factory --body "$(scripts/tf.sh baseline output -raw ecr_services_prefix)/factory"
```

`ECR_REPOSITORY` must be `<services prefix>/factory`: the platform's services ApplicationSet deploys `image.repository` from that path, and the runner and egress images are pushed beside it with `-runner` and `-egress` suffixes. Pull requests build the images without pushing and need none of these.
