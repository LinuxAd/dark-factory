# Dark Factory PoC — Specification

Status: agreed 2026-10-17, no phase started. Supersedes `../Dark Factory PoC — Project Plan.md` wherever the two disagree.

The factory takes an idea written as a GitHub issue and produces a public GitHub repo whose code passes hidden acceptance scenarios, with no human writing code. A human signs off at three gates. The PoC must show two things: that the factory beats a single Claude Code session, and that approved patterns make later runs of the same problem type cheaper.

## 1. Start here

Build the phases in order (§9). Each phase starts in plan mode with its exit criterion pasted in, then an ADR in `docs/adr/`, then the code, then a test or recorded demo that proves the exit criterion. A phase is done when its exit criterion has been shown on the real cluster, not just in unit tests. Do not start Phase 4 until the Phase 2 operator PR has been merged by Adam.

Current position: Phase 0, nothing built.

## 2. Success measures

The PoC is successful when the Phase 5 report shows all three:

1. **End to end.** Each of the three reference ideas (§8) reaches a Gate 2 PR that passes its hidden scenarios.
2. **Beats one session.** For each reference idea, the factory's tokens, cost and wall-clock time sit next to a baseline: one unattended Claude Code session given the same merged `SPEC.md`, with the same hidden scenarios run against its output. The report states which one passed more scenarios and at what cost. *Why:* if one session does as well for less, the pipeline isn't earning its complexity.
3. **Learns by transfer.** For each problem type, a sibling idea (a different idea of the same type) is run 3 times with an empty pattern library and 3 times with the approved patterns from the reference idea. Median tokens and median verify loops are lower with patterns. *Why:* re-running the same idea only measures memorisation, and a single run is too noisy to show anything.

## 3. Scope

In scope: the pipeline in §4, generated code in Go or TypeScript, one user (Adam), the existing `platform-project` cluster.

Out of scope for v1, with when each is revisited:

| Deferred | Revisit |
|---|---|
| Restate or Temporal workflow engine | When parallel fan-out or retry logic outgrows one reconciler loop |
| pgvector, embedding service, embedding benchmark | When approved patterns for one stage go over its context-pack budget (§6.3) |
| Loki, Tempo | When agent transcripts in S3 stop being enough to debug a run |
| Parallel implementers | After the Phase 5 report |
| SLO targets (tracked as metrics in v1, §7.6) | v2, using Phase 5 data as the baseline |
| Teardown with in-flight runs, Restate snapshots, `--accept-data-loss`, monthly restore drill | v2 |
| Dogfooding (the factory changing an existing repo, including itself) | v2; needs a brownfield mode for Plan, scenarios and token scoping |
| Run-status view in Backstage | v2; needs a custom plugin |
| Non-Anthropic providers, Bedrock in production | Decided in Phase 5 (§7.3) |
| Deploying or running generated projects | Not planned |
| Multi-tenancy, production hardening, a bespoke UI | Not planned |

## 4. Pipeline

One run per idea. Each stage reads its inputs from Postgres or Git and writes its outputs to the same places, so any stage can be re-run from its inputs.

| # | Stage | Runs where | Model | Output |
|---|---|---|---|---|
| 1 | Intake | Webhook handler | — | `ideas` row and `runs` row from an issue labelled `idea` in `factory-specs` |
| 2 | Clarify | Orchestrator | Opus | Up to 5 questions as comments on the idea issue; waits for Adam's replies |
| 3 | Spec | Orchestrator | Opus | `ideas/<slug>/SPEC.md`: behaviour, interfaces, non-functionals, plus `problem_type` and `language` |
| 4 | Research | Orchestrator | Opus + Anthropic web search tool, max 10 searches | `ideas/<slug>/RECOMMENDATIONS.md`: each open question, its options, a recommendation, cited URLs; claims without a source are marked as assumptions |
| 5 | Scenarios | Orchestrator | Opus | `ideas/<slug>/acceptance/`: executable tests (§4.2) |
| 6 | **Gate 1** | Adam | — | One PR to `factory-specs` holding stages 3–5. Adam decides the open points, edits as needed and merges |
| 7 | Plan | Orchestrator | Opus | Task DAG (§4.3); the orchestrator creates the private generated repo |
| 8 | Implement | Sandbox Job | Sonnet, Agent SDK | One task branch per task, merged into `factory/<slug>` |
| 9 | Verify | Sandbox Job, then orchestrator | Opus reviewer | Pass, or failures attributed to tasks (§4.4) |
| 10 | **Gate 2** | Adam | — | PR `factory/<slug>` → `main` on the generated repo; Adam merges |
| 11 | Go public | Orchestrator | — | Full-history `gitleaks`, copy `SPEC.md` to `docs/` and the scenarios to `acceptance/`, switch the repo to public |
| 12 | Retrospect | Orchestrator | Haiku via Message Batches | Candidate patterns and failure signatures as a PR to `patterns` |
| 13 | **Gate 3** | Adam | — | Adam merges the patterns PR; a Job triggered on merge re-indexes it into Postgres |

Retrospect runs on every terminal state: merged, abandoned or closed without merge.

### 4.1 Separation rule

Implementers never see the acceptance scenarios and cannot change paths the verifier owns. *Why:* without this, agents learn to make tests pass instead of meeting the spec.

Enforced by:
- Implementer Jobs get a GitHub token for the generated repo only, never for `factory-specs`. The orchestrator copies `SPEC.md` into their workspace.
- Only the verify Job receives `acceptance/`, mounted read-only from the orchestrator.
- Verify fails the run if the diff on `factory/<slug>` touches `acceptance/**` or `.github/**`. *Why:* CODEOWNERS doesn't stop pushes to a branch.

### 4.2 Acceptance scenarios

- Written after Research so they match the final spec, and reviewed by Adam at Gate 1.
- Executable in the idea's own test framework: Go tests with envtest for `k8s-controller`, Vitest for `polling-integration`, Playwright for `crud-web-app`.
- They test only the interfaces named in `SPEC.md`. A scenario that needs an internal symbol is a spec defect to be raised at Gate 1.

### 4.3 Plan

The task DAG is stored in `tasks`. Each task has an id, a description, its dependencies, and the file paths it owns. Implement runs tasks one at a time in topological order, and each task starts from the latest `factory/<slug>`. File ownership is recorded now so parallel runs can be turned on later without a schema change.

### 4.4 Verify and the loop cap

1. The deterministic checks run in a sandbox Job: build, lint, unit tests, `govulncheck` or `npm audit`, `gitleaks`, the owned-paths check (§4.1), then the hidden scenarios.
2. The Opus reviewer reads the spec, the diff and the failures. It attributes each failure to an existing task, which then loops back to Implement with the failure attached, or creates a new fix task.
3. Each task and each new fix task gets at most 3 loops.
4. When a cap is reached, the run moves to `paused` and the orchestrator opens an issue on `factory-specs` that offers three commands:
   - `/factory retry <hint>`: loops again with Adam's hint attached;
   - `/factory respec`: back to Gate 1 with the failures attached;
   - `/factory abandon`: terminal; Retrospect still runs.

Done when every check and scenario passes and the reviewer finds no spec mismatch. Implement and Verify then hand over to Gate 2.

### 4.5 Gate 2 review loop

A PR review comment that starts with `/factory fix` becomes a fix task on the same branch, then goes through Implement and Verify. Other comments are ignored. Merging is the only way past Gate 2.

## 5. Orchestration

### 5.1 State

There is no workflow engine. `runs` holds the current stage and status of each run (`pending`, `running`, `waiting_gate`, `paused`, `done`, `abandoned`), and `stage_runs` holds one row per attempt.

The orchestrator runs one reconciler loop: it picks up every run whose current stage is `pending` and whose inputs exist, and runs it. Every stage is idempotent: it writes its outputs, then marks itself complete in the same transaction as the move to the next stage. After a restart, the loop resumes each run from its last completed stage. *Why:* Postgres already has to survive teardown (§7.4), and keeping the journal in a second system adds a component without adding durability.

### 5.2 Human input

GitHub is the only input channel in v1.

| Event | Effect |
|---|---|
| Issue opened with label `idea` on `factory-specs` | Intake |
| Comment by Adam on an idea issue while Clarify is waiting | Answers recorded; Clarify continues when every question has a reply, or when Adam comments `/factory go` |
| PR merged on `factory-specs` | Gate 1 passed |
| PR merged to `main` on a generated repo | Gate 2 passed |
| PR closed without merge on a generated repo | Run abandoned |
| Comment `/factory retry\|respec\|abandon` on a pause issue | §4.4 |
| PR merged on `patterns` | Gate 3 passed; re-index Job starts |

Webhooks reach the orchestrator through the platform's Envoy Gateway at `factory.<platform domain>`, and every delivery is checked against the GitHub webhook HMAC secret. The orchestrator acts only on comments from Adam's GitHub account. Intake comes from the Backstage "Submit idea" template (it opens the issue) or `gh issue create`.

### 5.3 Notifications

A Discord webhook to a private channel, each message carrying the link, sent when:
- a gate is waiting for Adam;
- a run has paused at a loop cap;
- a budget was breached.

Nothing else is sent. Everything else lives on the dashboard.

## 6. Memory

### 6.1 Tiers

| Tier | Holds | Lifetime | Written by | Reused? |
|---|---|---|---|---|
| Working | Run state, task outputs | One run | Orchestrator | No |
| Episodic | `agent_calls`, transcripts in S3, stage outcomes | Whole PoC | Gateway, orchestrator | Only as evidence |
| Patterns | Approved spec fragments, task decompositions, code templates, ADRs | Until deprecated by a PR | Curator proposes, Adam approves at Gate 3 | Yes |
| Failure signatures | Error pattern, cause, the fix that worked | Until deprecated by a PR | Curator proposes, Adam approves at Gate 3 | Yes, as warnings |

### 6.2 Guarding against bad patterns

- The `patterns` Git repo is the source of truth, and Postgres only indexes it.
- A candidate pattern must cite a run that passed Verify. A failure signature may come from any run.
- `pattern_usage` records every run each pattern was used in and that run's outcome. A pattern used in 2 failing runs is flagged in the next Retrospect PR for Adam to review.

### 6.3 Retrieval in v1

1. Filter `patterns` and `failure_signatures` to `status = approved`, then by the run's `problem_type`, then by `language`.
2. Put every match into the stage's context pack, ordered by `success_count`. Stop at the pack budget: 8,000 tokens per stage to begin with.
3. Place the pack in the cacheable prompt prefix after the system prompt and tool definitions, and before the task.

When a stage's matches go over its budget, that is the trigger to build vector and keyword retrieval (§3).

### 6.4 Problem types

Defined in `patterns/problem-types.yaml`, and changed only by a PR to that repo. To begin with: `k8s-controller`, `polling-integration`, `crud-web-app`. The Spec stage assigns one, and Adam confirms it at Gate 1.

### 6.5 Core tables

`ideas`, `specs`, `runs`, `stage_runs`, `tasks`, `agent_calls`, `artifacts`, `patterns`, `pattern_usage`, `failure_signatures`, `budgets`.

`agent_calls` has one row per model request:
- run, task and stage;
- model;
- input, output, cache-read and cache-write tokens;
- cost in USD;
- latency;
- HTTP status;
- the S3 key of the transcript.

Every `patterns` row records the commit in `patterns` it was indexed from.

## 7. Platform

### 7.1 Placement

The factory runs on `~/repos/platform-project`, which already provides EKS, Karpenter, Argo CD, External Secrets, Envoy Gateway, the CloudNativePG operator, Prometheus, Grafana and the OTel Collector. The factory adds:

| Where | What |
|---|---|
| `platform-project/terraform/01-baseline` | S3 bucket `factory-state` (versioned, `prevent_destroy`, lifecycle on old base backups) and its KMS key. Secrets Manager entries: Anthropic key, GitHub App key, webhook secret, Discord webhook URL. *Why baseline:* it persists through `make down` |
| `platform-project/gitops` | One Argo CD Application pointing at this repo's `deploy/` |
| This repo | Everything else (§10) |

### 7.2 Components

| Component | Notes |
|---|---|
| Orchestrator (Go) | Reconciler loop, webhook handler, GitHub App client. Pod Identity limited to `factory-state` |
| Gateway (Go) | §7.3 |
| CloudNativePG cluster | WAL archive and nightly base backup to `factory-state`; on `make up` it bootstraps from the latest backup |
| Runner image | Agent SDK, Go and Node.js toolchains |
| Verify image | Runner image plus envtest assets and Playwright browsers baked in |
| `sandbox` node pool | Karpenter spot, scales to zero, tainted; only runner and verify Jobs tolerate the taint |
| Egress proxy | Allowlist: `proxy.golang.org`, `sum.golang.org`, `registry.npmjs.org`, `github.com`, `api.github.com`, `*.githubusercontent.com` |
| GitHub App | Installed on `factory-specs`, `patterns` and generated repos. Creates repos and issues one-hour tokens scoped to one repo |
| Grafana dashboards | Read `agent_calls` from Postgres directly |

### 7.3 Model gateway

Every model call goes through the gateway: orchestrator stages call it directly, and the Agent SDK in sandbox Jobs reaches it via `ANTHROPIC_BASE_URL`. The gateway is the only holder of a model credential. *Why:* the costliest stage gets metered too, kill switches live in one place, and a pod running `npm` code never sees the key.

- It passes Anthropic Messages requests through untranslated, streaming included, so caching headers, beta headers and server tools keep working.
- Each caller sends an `X-Factory-Run`, `X-Factory-Task` and `X-Factory-Stage` header, and the gateway rejects requests that lack them. Sandbox Jobs get a per-Job bearer token minted by the orchestrator, and the gateway maps that token to its run and task.
- It reads `usage` from each response or stream and writes one `agent_calls` row.
- It enforces budgets before forwarding (§7.5).
- The upstream is set in config: `anthropic` in v1. A `bedrock` upstream (SigV4 with a Pod Identity role; the Agent SDK side uses `CLAUDE_CODE_USE_BEDROCK=1`, `CLAUDE_CODE_SKIP_BEDROCK_AUTH=1` and `ANTHROPIC_BEDROCK_BASE_URL`) is built and compared in Phase 5. *Why not Bedrock now:* Claude on Bedrock has no server-side web search, which Research needs, and AWS Budgets only alerts where the Console spend limit stops spending.
- LiteLLM replaces this gateway only if a second provider is actually added.

### 7.4 Teardown in v1

`make down` is allowed only when no run has a stage in `running`. Runs that are `waiting_gate` or `paused` are fine. A `factory-quiesce` check refuses with a list of running stages; otherwise it forces a CloudNativePG backup and confirms the backup completed. After `make up`, CloudNativePG restores from `factory-state` and the reconciler resumes runs (§5.1).

### 7.5 Budgets

| Control | Value | Effect when hit |
|---|---|---|
| Per run | $30 to begin with | Gateway rejects; run moves to `paused`; Discord alert |
| Per day | $60 to begin with | Gateway rejects all calls until UTC midnight; Discord alert |
| Over estimate | 150% of the median cost of past completed runs of the same problem type | Run paused; Discord alert. Active only once a type has 2 or more completed runs |
| Console spend limit | Just above 31 × the daily cap | Anthropic stops serving; backstop independent of factory code |

### 7.6 Sandbox Job controls

- No Pod Identity role, so no AWS access.
- Default-deny NetworkPolicy; egress only to the egress proxy and the gateway.
- `npm ci --ignore-scripts`, unless the package is on the spec's `allow_install_scripts` list.
- Pod Security `restricted`; read-only root filesystem except the workspace and cache volumes; CPU and memory limits; `activeDeadlineSeconds`.
- Spot interruptions: a Job that dies is re-run from the task's starting commit. Its partial work is discarded.

### 7.7 Tracked metrics (no targets in v1)

- Share of runs passing hidden scenarios.
- Lead time from Gate 1 merge to Gate 2 PR ready.
- Cost and tokens per run and per stage.
- Cache hit ratio.
- Verify loops per task.
- Pattern reuse rate per problem type.

## 8. Reference ideas

Run them in this order.

| Idea | Type | Stack | Sibling for the transfer test |
|---|---|---|---|
| Backstage annotation operator: reads team ownership and alert criticality from the Backstage catalog and keeps matching annotations on the service's workloads | `k8s-controller` | Go, controller-runtime | Defined in Phase 4 |
| Gillingham FC news to Discord. Webhook or discord.js, as Research recommends. `~/repos/discord-bot` is a reference for scope only | `polling-integration` | TypeScript | Defined in Phase 4 |
| Peacehaven community news site: local news in a more structured format than Facebook groups, with sign-in and moderation as Research recommends | `crud-web-app` | TypeScript web app + Postgres | Defined in Phase 4 |

## 9. Phases

About 10 working weekends.

| Phase | Weekends | Builds | Exit criterion |
|---|---|---|---|
| 0 Foundations | 1 | Repo skeleton, `CLAUDE.md`, ADRs, `01-baseline` additions, CloudNativePG cluster with S3 backup and restore, `agent_calls`, gateway, `sandbox` pool, egress proxy | A sandbox Job calls Claude through the gateway with no key in the pod, the call appears in `agent_calls`, and the row is still there after `make down` then `make up` |
| 1 Spec pipeline | 2 | Webhooks, reconciler, Intake, Clarify, Spec, Research, Scenarios, Gate 1, Backstage template, Discord alerts | The operator idea goes from issue to merged `factory-specs` PR containing spec, recommendations and scenarios |
| 2 Build pipeline | 3 | Plan, repo creation, runner and verify images, Implement, Verify, loop cap and pause, Gate 2, Go public | The operator PR passes its hidden scenarios, Adam merges, the repo is public, and the run's costs are recorded as the first-run baseline |
| 3 Cost visibility | 1 | Grafana dashboards, budget enforcement and alerts | Cost and tokens per stage per run on a dashboard; a test run blocked by a lowered per-run cap |
| 4 Long-term memory | 2 | Retrospect, Gate 3, re-index Job, retrieval and context packs, `pattern_usage`; define the 3 sibling ideas | The operator sibling run with patterns beats the run without, median of 3 each |
| 5 Evaluate | 1 | Bot and site reference ideas, their siblings, Claude Code baselines, Bedrock upstream comparison | The report answers the three success measures (§2) and records the provider decision |

## 10. Repo layout and build rules

- `orchestrator/`: Go module with `cmd/orchestrator` and `cmd/gateway`.
- `runner/`: Dockerfiles for the runner and verify images.
- `memory/`: SQL migrations and retrieval queries.
- `deploy/`: Kubernetes manifests synced by Argo CD.
- `docs/adr/`: one ADR per decision.
- `patterns` and `factory-specs` are separate GitHub repos.

Rules for building the factory with Claude Code, recorded in `CLAUDE.md`:
- Make a failing test pass by changing the code under test. Change a test only when the spec it encodes changed, and say so in the commit. *Why:* the factory forbids its agents from gaming tests, so the factory itself must not be built that way.
- Hooks run `golangci-lint`, `go test` and `terraform validate`, so the factory is built under the same verify discipline it enforces.
- Agents never push to `main` on any repo. Every merge to `main` is Adam's.

## Next action

Start Phase 0 in plan mode with its exit criterion. The first step: write `CLAUDE.md` from §1, §4.1 and §10 of this spec.
