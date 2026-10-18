#!/usr/bin/env bash
# make smoke (plan Step 7, AC6): one Agent SDK call from a sandbox Job through
# the gateway, then the agent_calls rows it produced.
#
# Exits 0 only if the Job succeeded, at least one row exists for the run, every
# row has a cost, and at least one row has input and output tokens above 0.
#
# Usage: FACTORY_KUBE_CONTEXT=<ctx> scripts/smoke.sh
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require_context

ts="$(date -u +%Y%m%d%H%M%S)"
run_id="phase0-smoke-${ts}"
job="${run_id}"
secret="${run_id}-token"

# shellcheck disable=SC2329 # invoked by the EXIT trap.
cleanup() {
  kc --namespace "${FACTORY_SANDBOX_NAMESPACE}" delete secret "${secret}" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "minting a gateway token for run ${run_id}"
token="$(mint_token "${run_id}" smoke phase0-smoke 1h)"
create_token_secret "${FACTORY_SANDBOX_NAMESPACE}" "${secret}" "${token}"
unset token

job_ok=true
run_job "${FACTORY_SANDBOX_NAMESPACE}" "${job}" smoke "${secret}" '["node", "/opt/runner/smoke/smoke.mjs"]' || job_ok=false

echo
echo "agent_calls rows for run ${run_id}:"
psql_factory -v run_id="${run_id}" <<'SQL'
SELECT id, run_id, task_id, stage, model, input_tokens, output_tokens,
       cache_read_tokens, cache_write_tokens, server_tool_requests, cost_usd,
       latency_ms, http_status, created_at
FROM agent_calls
WHERE run_id = :'run_id'
ORDER BY id;
SQL

summary="$(psql_factory -A -t -F ' ' -v run_id="${run_id}" <<'SQL'
SELECT count(*),
       count(*) FILTER (WHERE cost_usd IS NULL),
       count(*) FILTER (WHERE input_tokens > 0 AND output_tokens > 0)
FROM agent_calls
WHERE run_id = :'run_id';
SQL
)"
read -r rows unpriced with_tokens <<<"${summary}" || true
rows="${rows:-0}" unpriced="${unpriced:-0}" with_tokens="${with_tokens:-0}"

status=0
[[ "${job_ok}" == true ]] || { log "FAIL: the smoke Job did not succeed"; status=1; }
((rows >= 1)) || { log "FAIL: no agent_calls row for ${run_id}"; status=1; }
((unpriced == 0)) || { log "FAIL: ${unpriced} row(s) have no cost_usd"; status=1; }
((with_tokens >= 1)) || { log "FAIL: no row has input and output tokens above 0"; status=1; }
if ((status == 0)); then
  log "smoke OK: ${rows} row(s) for ${run_id}; note an id for make verify-row ID=<id>"
fi
exit "${status}"
