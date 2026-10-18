# Dark Factory

The factory turns an idea written as a GitHub issue into a GitHub repo whose code passes hidden acceptance scenarios, with no human writing code. `SPEC.md` is the source of truth; read it before changing behaviour.

## How work proceeds (SPEC.md §1)

- Build the phases in order (SPEC.md §9). Current position: Phase 0, Foundations.
- Each phase goes in this order: plan mode with the phase's exit criterion pasted in, an ADR in `docs/adr/`, the code, then a test or recorded demo that proves the exit criterion.
- A phase is done when its exit criterion has been shown on the real cluster. Unit tests alone do not finish a phase.
- Do not start Phase 4 until Adam has merged the Phase 2 operator PR.

## Separation rule (SPEC.md §4.1)

Implementers never see the acceptance scenarios, and they cannot change paths the verifier owns. Without this rule, agents learn to make tests pass instead of meeting the spec.

- Implementer Jobs get a GitHub token for the generated repo only, never for `factory-specs`. The orchestrator copies `SPEC.md` into their workspace.
- Only the verify Job receives `acceptance/`, mounted read-only.
- Verify fails the run if the diff on `factory/<slug>` touches `acceptance/**` or `.github/**`.

## Repo layout (SPEC.md §10)

| Path | What |
|---|---|
| `orchestrator/` | Go module: `cmd/gateway` (Phase 0) and `cmd/orchestrator` (Phase 1) |
| `memory/` | SQL migrations and retrieval queries (its own Go module, embedded by the gateway) |
| `runner/` | Dockerfiles for the runner, verify and egress-proxy images |
| `deploy/chart` | Helm chart Argo CD syncs into namespace `factory` (namespaced resources only) |
| `deploy/sandbox`, `deploy/seed` | Manifests applied by `make` targets, not by Argo CD |
| `scripts/` | Bash helpers behind the top-level `Makefile` |
| `docs/adr/` | One ADR per decision |

`patterns` and `factory-specs` are separate GitHub repos. Cluster-scoped and IAM pieces live in `~/repos/platform-project` (ADR 0001, Decision A).

## Build rules (SPEC.md §10)

- Make a failing test pass by changing the code under test. Change a test only when the spec it encodes has changed, and say so in the commit message. The factory forbids its agents from gaming tests, so the factory must not be built that way.
- Hooks in `.claude/settings.json` run `golangci-lint`, `go test` and `terraform validate`, so the factory is built under the same verify discipline it enforces.
- Go and golangci-lint run in Docker images pinned in `orchestrator/Makefile`, so use `make -C orchestrator lint test build`, not a local toolchain. Docker must be running.
- `make lint` checks the shell scripts and the chart (shellcheck, helm lint, kubeconform).
- Agents never push to `main` on any repo. Adam makes every merge to `main`.

## Cluster safety

- Every script that talks to the cluster refuses to run unless `FACTORY_KUBE_CONTEXT` is set. It passes `--context` on every call. Never point it at a cluster unless Adam has asked you to.
- Bring-up and teardown are platform-project's `make up` and `make down`. This repo never runs them.
- The Anthropic key exists only in Secrets Manager and in the gateway process. Never put it in a Secret, an env var, a file or a log.
