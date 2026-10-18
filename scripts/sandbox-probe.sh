#!/usr/bin/env bash
# make sandbox-probe [CONTROL=1] (plan Step 7, AC4).
#
# Default: runs runner/probe/probe.sh in a sandbox Job in factory-sandbox and
# exits with its result; 0 means every isolation check passed.
#
# CONTROL=1: runs the same pod spec in a throwaway namespace
# factory-probe-control-<ts> that has the Pod Security restricted label but no
# NetworkPolicies, then deletes it. There the probe is expected to fail (this
# target exits non-zero), which shows the policies are what isolate the pod.
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

# shellcheck disable=SC2329 # invoked by the EXIT trap.
cleanup() {
  if [[ "${control}" == 1 ]]; then
    log "deleting control namespace ${namespace}"
    kc delete namespace "${namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  else
    kc --namespace "${namespace}" delete secret "${secret}" --ignore-not-found >/dev/null 2>&1 || true
  fi
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

log "minting a gateway token for run ${run_id}"
token="$(mint_token "${run_id}" probe phase0-probe 1h)"
create_token_secret "${namespace}" "${secret}" "${token}"
unset token

status=0
logfile="$(mktemp)"
run_job "${namespace}" "${job}" probe "${secret}" '["/opt/runner/probe/probe.sh"]' 2>&1 | tee "${logfile}" || true
grep -q "Job ${job} succeeded" "${logfile}" || status=1

if [[ "${control}" == 1 ]]; then
  if ((status != 0)) && grep -q '^FAIL direct-anthropic' "${logfile}"; then
    log "control: the probe failed as expected (direct egress was open without the policies)"
  elif ((status != 0)); then
    log "control: the probe failed, but not on direct egress; read the report above"
  else
    log "control: UNEXPECTED: the probe passed without NetworkPolicies, so this run does not show enforcement"
  fi
fi
rm -f "${logfile}"
exit "${status}"
