#!/usr/bin/env bash
# make sandbox-probe [CONTROL=1] (plan Step 7, AC4).
#
# Default: runs runner/probe/probe.sh in a sandbox Job in factory-sandbox and
# exits with its result; 0 means every isolation check passed.
#
# CONTROL=1: runs the same pod spec in a throwaway namespace
# factory-probe-control-<ts> that has the Pod Security restricted label but no
# NetworkPolicies, then deletes it. The Job's wait-for-policy init container is
# left out there (it could only time out), so the probe itself meets the open
# egress. Exit codes in control mode:
#   1  expected (AC4): the probe ran and reported FAIL direct-anthropic, which
#      shows the policies are what isolate the pod
#   3  inconclusive: the Job failed for another reason (image pull, scheduling,
#      the probe never ran, or it failed only on other checks)
#   0  unexpected: the probe passed without NetworkPolicies
# Deleting a policy in factory-sandbox would prove nothing: a pod selected by
# any egress policy stays isolated, and Argo CD self-heal restores it.
#
# Usage: FACTORY_KUBE_CONTEXT=<ctx> [CONTROL=1] scripts/sandbox-probe.sh
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require_context

ts="$(date -u +%Y%m%d%H%M%S)"
control="${CONTROL:-0}"
run_id="phase0-probe-${ts}"
job="${run_id}"
secret="${run_id}-token"

if [[ "${control}" == 1 ]]; then
  namespace="factory-probe-control-${ts}"
else
  namespace="${FACTORY_SANDBOX_NAMESPACE}"
fi

# EXIT trap: revoke the run's gateway token and remove the Secret (or the
# control namespace), all best-effort, then exit with the script's own status.
# shellcheck disable=SC2329 # invoked by the EXIT trap.
cleanup() {
  local rc=$?
  revoke_token "${run_id}"
  if [[ "${control}" == 1 ]]; then
    log "deleting control namespace ${namespace}"
    kc delete namespace "${namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  else
    kc --namespace "${namespace}" delete secret "${secret}" --ignore-not-found >/dev/null 2>&1 || true
  fi
  [[ -z "${logfile:-}" ]] || rm -f "${logfile}"
  exit "${rc}"
}
trap cleanup EXIT

if [[ "${control}" == 1 ]]; then
  log "creating control namespace ${namespace} (no NetworkPolicies)"
  kc apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
  labels:
    app.kubernetes.io/part-of: factory
    factory/purpose: probe-control
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sandbox
  namespace: ${namespace}
automountServiceAccountToken: false
EOF
fi

# 1h: well inside the gateway's 2h mint-token cap; revoked on exit anyway.
log "minting a gateway token for run ${run_id}"
token="$(mint_token "${run_id}" probe phase0-probe 1h)"
create_token_secret "${namespace}" "${secret}" "${token}"
unset token

wait_for_policy=1
[[ "${control}" == 1 ]] && wait_for_policy=0
logfile="$(mktemp)"
run_job "${namespace}" "${job}" probe "${secret}" '["/opt/runner/probe/probe.sh"]' "${wait_for_policy}" 2>&1 |
  tee "${logfile}" || true

if [[ "${control}" != 1 ]]; then
  grep -q "Job ${job} succeeded" "${logfile}" || exit 1
  exit 0
fi

if grep -q '^probe: pod ' "${logfile}" && grep -q '^FAIL direct-anthropic' "${logfile}"; then
  log "control: the probe failed as expected (direct egress was open without the policies)"
  exit 1
fi
if grep -q "Job ${job} succeeded" "${logfile}"; then
  log "control: UNEXPECTED: the probe passed without NetworkPolicies, so this run does not show enforcement"
  exit 0
fi
log "control: inconclusive: the Job failed, but not because the probe found direct egress open (image pull, scheduling, the probe never ran, or other checks failed); read the report above"
exit 3
