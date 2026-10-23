# Laptop runtime with local models — design

Date: 2026-10-22. Status: agreed with Adam 2026-10-23.

This changes where the factory runs and which model serves each stage. Where it conflicts with `SPEC.md`, this document wins once it is approved; `SPEC.md` and `CLAUDE.md` are then updated to match (see "Documents to change").

## Intent

Adam wants the factory to run on his laptop as its main home, with the EKS platform cluster kept as an occasional, paid-for alternative. Running on the laptop also changes which models do the work:

- **Thinking stays on Claude via the Anthropic API.** Interpreting the request and planning the work are where a weak model hurts most, and a model that runs on the laptop can't do them well.
- **Grunt work runs on local models.** Implementation, and the critics that check it, run on models served by llama.cpp on the laptop.

This is a proof of concept and an experiment, not a production-quality software factory. Its purpose is to show Adam's ability to build something with AI. The local-model build goes ahead whatever the spike shows about quality. Getting the model sized right for the machine is what matters most.

The bet being tested: Claude does the interpretation and the plan, and local models do the build and review. If that doesn't produce a workable application or feature, Adam will consider moving more stages to cloud models. That decision is out of scope here, but the design has to make it cheap. A stage's model is configuration, and every call is metered the same way, so the evidence exists when the question comes up.

## Goals

1. The whole factory (gateway, orchestrator, Postgres, egress proxy, sandbox Jobs) runs on the laptop under Colima.
2. Clarify, Spec, Research, Scenarios and Plan call Claude through the gateway.
3. Implement and the critics call a local model through the same gateway, with every call written to `agent_calls`.
4. Implementation and critique are separate steps run by separate model roles. A critic's fail sends the task back to Implement with the critics' instructions attached.
5. The EKS deployment path still works from the same chart.

## Non-goals

- Postgres backups or durability beyond a Colima volume on the laptop.
- Running implementer and critic models side by side. v1 swaps them in and out of memory.
- Speed. A slower, stronger local model is preferred over a fast, weak one. A build-and-review phase that takes hours is acceptable.
- Choosing which extra stages move to the cloud if the local build doesn't work.
- Parallel implementers (still deferred, SPEC.md §3).

## Anti-goals

These would make it a failure even if it ran:

- **Weakening the separation rule (SPEC.md §4.1).** Implementers and critics still never see `acceptance/`, and the owned-paths check still runs.
- **A model credential in a sandbox pod.** Local models need no key, but the Anthropic key still lives only in the gateway.
- **Model choice hard-coded into the pipeline.** Swapping a stage between local and cloud must be a config change, not a code change. Otherwise the "consider more cloud models" decision gets expensive.
- **Local calls missing from `agent_calls`.** The Phase 5 comparison needs tokens and wall-clock time for every call, local ones included.

## Constraints

- **Hardware:** Apple M3 Pro, 18 GB unified memory. The Colima VM currently has 4 CPUs and 8 GiB.
- **llama.cpp runs natively on macOS with Metal, not inside Colima.** A Colima VM has no Apple GPU access, so in-VM inference is CPU-only and several times slower. Containers reach it over HTTP at `host.docker.internal` (or the Colima host address).
- **Memory budget (sizing is paramount):** model weights, the KV cache, the Colima VMs and macOS share 18 GB. By default macOS lets the GPU wire only about 12 GB; that limit can be raised with `sysctl iogpu.wired_limit_mb`. A 30B model at 4-bit (about 18.5 GB as Q4_K_M) does not fit. Once Colima has 4–6 GB, roughly 10 GB remains for weights plus KV cache. That means either a ~30B mixture-of-experts model at 2–3 bit or a ~14B dense coding model at 4-bit, with a moderate context. Size the VMs to the smallest that runs the stack, and size model and context to what remains. Measure this, don't estimate it.
- Go and lint run in pinned Docker images (`CLAUDE.md`); Docker is Colima's.

## Scope and phase order

Phases are still built in order (SPEC.md §1). This design therefore splits into two parts:

- **Built now, as a re-run of Phase 0 on the laptop:** the spike, ADR 0002, the laptop chart profile, the gateway's `local` upstream and per-token model rules, and the laptop Phase 0 exit.
- **Built later:** the stage-to-model map, pi in Implement, and the critics in Verify. These become changes to how Phases 1 and 2 are written into `SPEC.md`, and get built when those phases are.

## Decided by Adam

| Decision | Detail |
|---|---|
| Laptop is the main runtime | Colima. EKS stays as a backup option. |
| Cloud for thinking | Interpreting the request and planning use Anthropic models via the API. |
| Local for grunt work | Implementation and review use on-device models served by llama.cpp. |
| Separate critic step | Local critic models check the diff against the plan and review the implementation, as a step separate from implementation. A fail restarts the implementation loop with the critics' instructions for the required changes. |
| llama.cpp on Metal, called over HTTP | Runs natively on the Mac, outside Colima. |
| Model swapping is fine for v1 | Implementer and critic models load and unload as needed. |
| No Postgres backups on the laptop | |
| Slower and stronger | Prefer the stronger local model even if a run takes hours. |
| Local models regardless | The local-model build goes ahead as a PoC even if quality is weak. There is no go/no-go gate and no move to Sonnet. Right-sizing the model to the machine is paramount. |
| Factory gets its own Colima profile | No home-directory mount. The Anthropic key lives in macOS Keychain, and `make local-up` copies it into a Secret that only the gateway mounts. The `CLAUDE.md` key rule is rewritten for the laptop. |
| This repo brings up the laptop cluster | `make local-up` installs the cluster-wide prerequisites (CloudNativePG operator, monitoring CRDs) on the laptop only. EKS bring-up stays in platform-project. |
| Implementer harness is pi | The pi coding agent (`@mariozechner/pi-coding-agent`, badlogic/pi-mono) replaces the Claude Agent SDK in implementer Jobs. |

## Design

### Runtime: k3s in Colima

Colima runs with Kubernetes enabled (k3s). The existing chart deploys into it with a `values-local.yaml` profile. The EKS path keeps the current values and Argo CD. *Why k3s rather than Compose:* the chart, the sandbox namespace, Jobs and NetworkPolicies (which k3s enforces) carry over almost unchanged, and so does the separation rule.

| EKS piece | Laptop stand-in |
|---|---|
| Argo CD | `make local-up`, which runs `helm upgrade --install` against the Colima context |
| ECR | Images built straight into Colima's runtime (`imagePullPolicy: Never` or `IfNotPresent` with local tags) |
| CloudNativePG + Barman to S3 | CloudNativePG on a k3s local-path PVC, no `ObjectStore`, no `ScheduledBackup`, no recovery bootstrap. Plain `initdb` on first start. Data survives Colima restarts and is lost on `colima delete` |
| Secrets Manager + Pod Identity | The Anthropic key in macOS Keychain. `make local-up` reads it with `security find-generic-password` and creates a Secret that only the gateway mounts. The key is never written to a file. This replaces the "only in Secrets Manager" rule in `CLAUDE.md` for the laptop and is recorded in ADR 0002 |
| platform-project bring-up | `make local-up` in this repo installs the cluster-wide prerequisites (CloudNativePG operator, the ServiceMonitor CRD or monitoring stack) into the factory's k3s, then the chart. This exception to "this repo never runs bring-up" applies to the laptop only |
| Karpenter `sandbox` spot pool, taints | Not used. Jobs run on the single k3s node. Tolerations stay harmless |
| `FACTORY_KUBE_CONTEXT` guard | Unchanged. Set it to the Colima context (`colima`) |

**Isolation from the rest of the laptop.** Generated npm and Go code now runs on Adam's personal machine instead of throwaway AWS nodes.

- **Dedicated profile.** The factory runs in its own Colima profile (`colima start --profile factory --kubernetes`) with no home-directory or other host mounts. SSH keys, Keychain and the repo checkouts are therefore not visible inside the VM.
- **Existing VM unchanged.** Adam's default Colima VM keeps its home mount for `make -C orchestrator lint test build`.
- **Memory.** The two VMs share the memory budget. Stop the default VM while a run is going if memory is short.
- **Images.** They are built in the default VM and loaded into the factory profile (for example `docker save | colima ssh --profile factory -- sudo k3s ctr images import -`). The builder may find a cleaner route.

The chart branches on one value (for example `platform: local|eks`) rather than duplicating templates. `make lint` renders and validates both profiles.

### Model serving on the host

- `llama-server` runs on macOS with Metal and serves the local models over HTTP. It speaks the Anthropic Messages API (`POST /v1/messages`, streaming, tool use), which llama.cpp added in early 2026.
- **Loading on demand:** either llama-server's router mode (`--models-preset`, `--models-max 1`) or llama-swap in front of it. The builder chooses after the spike. Whichever is used, a request names a model and the server loads it, evicting the other.
- **Starting point for models:** the spike chooses, within the memory budget. Candidates are Qwen3-Coder-30B-A3B at 2–3 bit, or a ~14B dense coding model at 4-bit, for the implementer. For the critics, either the same weights or a similarly sized general/reasoning model. Model names, quantisation and context length live in config, not code.
- **Running it:** a `make models-up` target (or a launchd plist) starts it, and `make factory-status` reports whether it is reachable from inside the cluster.
- **Binding:** it listens on an address the Colima VM can reach. My call: it binds only to the address Colima's VM reaches the host on, never `0.0.0.0` on the LAN. If the setup can't avoid a wider bind, it must at least require llama-server's `--api-key`, held by the gateway.

### Gateway: two upstreams, routed by model

The gateway stays the only path to any model.

- **Routing:** it gains a second upstream, `local`. The route is chosen by the request's `model` field: a config map from model-name prefix to upstream. Anything not mapped goes to `anthropic`. *Why by model rather than by stage:* the stage-to-model mapping then lives in one place (the orchestrator's stage config), and moving a stage to the cloud is a one-line config change.
- **Format:** requests to `local` are still Anthropic Messages, passed through untranslated, so the existing usage parsing and streaming code applies.
- **Metering:** `agent_calls` rows for local calls record tokens and latency, with cost `0`. They carry a known zero price, not "unpriced", so they don't trip the unpriced-model metric. The `model` column records the local model id, which makes local and cloud calls distinguishable.
- **Budgets:** budget checks still run. Local calls add nothing to spend.
- **Per-token model rules (my call):** a sandbox Job's token is minted with the stage it serves. The gateway allows that token only the models configured for that stage, and rejects any other model, including unmapped ones, with a 403 that it logs. *Why:* without this, a misconfigured pi or untrusted code holding the token could quietly send grunt work to Claude, which spends money and corrupts the local-versus-cloud evidence. The "unmapped goes to Anthropic" fallback applies only to orchestrator calls. This adds a column to `gateway_tokens` and a flag to `mint-token`.
- **Token lifetime (my call):** the 2-hour cap from ADR 0001 Decision D stays. Each implement attempt and each critic step is its own Job, with `activeDeadlineSeconds` under the token's TTL, so a multi-hour loop is several short Jobs. If the spike shows one attempt can't finish inside 2 hours, that goes back to Adam as a security decision, not a silent raise.
- **Credentials:** the Anthropic key is never sent to the local upstream. Use the local `--api-key` if there is one, otherwise no credential.
- **Network policy:** the gateway's NetworkPolicy egress gains the host address and port of llama-server.
- **Sandbox Jobs:** unchanged. They reach only the gateway and the egress proxy, never llama-server directly.

### Stage-to-model map

This replaces the Model column of SPEC.md §4. It lives in orchestrator config.

| Stage | Model | Where |
|---|---|---|
| Clarify, Spec, Research, Scenarios | Opus (unchanged) | Anthropic |
| Plan | Opus (unchanged) | Anthropic |
| Implement | Local implementer model, driven by pi | llama-server |
| Critique (new, part of Verify) | Local critic model(s) | llama-server |
| Retrospect | Local model (my call: its output is approved by Adam at Gate 3 anyway) | llama-server |

The Research stage's web search is an Anthropic server tool, so Research must stay on Anthropic.

**On EKS (my call):** EKS has no llama-server, so the stage map is set per platform profile. On EKS, Implement and the critics use Sonnet and Retrospect uses Haiku, with pi still driving Implement through the gateway. pi therefore has to be shown working against Claude once as well as against llama-server. "EKS still works" means the chart renders and lints for EKS and the stage map has an EKS profile. Running a full EKS demo is not required by this design.

### Implement with pi

- **Runner image:** the implementer Job's runner image installs pi from a lockfile, with `--ignore-scripts` as now. pi runs headless (print/JSON mode, or its RPC or SDK mode).
- **Gateway config:** pi's `models.json` defines one provider:
  - `api: "anthropic-messages"`
  - `baseUrl` pointing at the gateway
  - `apiKey` set to the per-Job gateway token
  - `headers` carrying `X-Factory-Run`, `X-Factory-Task` and `X-Factory-Stage` from the Job's environment

  pi supports all of these fields. The Job mounts the file read-only.
- **Prompt:** the implementer gets the task from the DAG, `SPEC.md`, the task's owned paths, and, on a retry, the critics' instructions and any deterministic-check failures. It never gets `acceptance/`.
- **Tools:** pi's built-in tools (read, write, edit, bash) run inside the sandbox Job as today. The egress proxy and the sandbox controls (SPEC.md §7.6) are unchanged.
- **Output:** a commit on the task branch, merged into `factory/<slug>` as SPEC.md §4.3 describes.

### Verify with local critics

Verify (SPEC.md §4.4) becomes:

1. **Deterministic checks**, unchanged: build, lint, unit tests, `govulncheck`/`npm audit`, `gitleaks`, the owned-paths check, then the hidden scenarios. These run in the verify Job.
2. **Critics**, run as a separate step after the checks:
   - **Plan critic.** Checks the task's diff against the plan's task description and the spec. Did it do what the task said, and only that?
   - **Code critic.** Reviews the implementation: correctness, obvious bugs, tests that don't test anything, gaming of checks.

   Each critic returns a structured verdict: `pass` or `fail`, with a list of required changes. Each change names a file or behaviour and gives an instruction the implementer can act on.
3. **Combined verdict.** The task passes only if the deterministic checks pass and both critics pass. Otherwise the failures and the critics' instructions are attached to the task, and it goes back to Implement.
4. **Loop cap and pause:** unchanged. At most 3 loops per task. At the cap the run pauses with `/factory retry|respec|abandon`.
5. **Failure attribution:** the Opus reviewer's job of attributing scenario failures to tasks or creating fix tasks moves to a local critic as well. My call, following "review is local".

**What the critics see:** the spec, the plan's task, the diff and the check results, including scenario failure output. They do not get the `acceptance/` source.

**Swapping cost:** critics run one after another. If the critic model differs from the implementer, each loop pays two model swaps. That's accepted for v1.

### Phase 0 exit on the laptop

The Phase 0 demo was never recorded on EKS (`docs/demos/phase0-exit.md` is still a template). So the laptop takes over that exit criterion, extended to cover both upstreams:

> A sandbox Job on Colima calls Claude and the local model through the gateway with no key in the pod. Both calls appear in `agent_calls`, with tokens and latency, and the local call costs $0. Both rows are still there after the factory pods restart and after `colima stop` then `colima start`.

The EKS exit criterion stays as written, for whenever EKS is used.

### First step: a throwaway spike

Before building any of the above, a spike on the host answers four questions:

1. Does pi, in headless mode with `api: anthropic-messages`, drive a llama-server model through to a working change on a small Go or TypeScript task?
2. Does llama-server's `/v1/messages` (router mode or behind llama-swap) return `usage` in the format the gateway's parser expects, streaming included? If not, the gateway needs a small adapter for local responses.
3. Which model and quantisation fit in memory next to a Colima VM big enough for k3s, Postgres and a runner Job, and at what context length?
4. Roughly how long does one implement-plus-critique loop take on a small task?

The spike sizes the model; it is not a go/no-go gate. It compares at least a ~30B mixture-of-experts model at 2–3 bit against a ~14B dense coding model at 4-bit, measuring memory, tokens per second and whether the task's tests pass. The best fit goes ahead whatever its quality. The spike's findings go into an ADR with the model choice. If pi can't drive the local model reliably, the fallback is the Claude Agent SDK pointed at the gateway (llama-server documents this setup), not a third harness.

## Left to the builder

- **Model serving:** router mode or llama-swap. Exact models, quantisation and context lengths, chosen from the spike.
- **pi invocation:** print/JSON, RPC or SDK mode.
- **Critic interface:** whether the critics are sandbox Jobs running pi with read-only tools, or single gateway calls from the orchestrator with the diff inline. My call if no evidence favours either: single calls, falling back to pi with read-only tools when a diff won't fit in context.
- **Critic prompts and verdict schema.**
- **Colima sizing,** within the memory budget above.
- **Chart profile mechanics** and `make` target names.

## Documents to change once approved

- **ADR 0002:** laptop runtime, local models, the key held in a file, routing by model. It records the spike's results.
- **`SPEC.md`:**
  - §3: non-Anthropic providers are now in scope for local models.
  - §4: the model column, and the Verify step with critics.
  - §4.4: verify and critics.
  - §7: placement on Colima, and the gateway with the local upstream.
  - §9: the Phase 0 exit on the laptop.
- **`CLAUDE.md`:** where the factory runs, the key rule rewritten for the laptop (Keychain to a gateway-only Secret), and the laptop exception to "this repo never runs bring-up".
- **`README.md`:** laptop bring-up first, EKS second.
