#!/usr/bin/env bash
# Shared helpers for the factory scripts. Source it; do not run it.
#
# Every kubectl call goes through kc(), which always passes an explicit
# --context. The scripts refuse to run unless FACTORY_KUBE_CONTEXT names a
# context in the local kubeconfig, so nothing reaches a cluster by accident.
#
# Environment:
#   FACTORY_KUBE_CONTEXT       required; the kubectl context of the platform cluster
#   FACTORY_NAMESPACE          default factory
#   FACTORY_SANDBOX_NAMESPACE  default factory-sandbox
#   AWS_PROFILE etc.           used as-is by the aws CLI (S3 prefix checks only)

FACTORY_NAMESPACE="${FACTORY_NAMESPACE:-factory}"
FACTORY_SANDBOX_NAMESPACE="${FACTORY_SANDBOX_NAMESPACE:-factory-sandbox}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

need() {
  local tool
  for tool in "$@"; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required but not on PATH"
  done
}

# require_context: the guard every script calls first.
require_context() {
  need kubectl jq
  [[ -n "${FACTORY_KUBE_CONTEXT:-}" ]] ||
    die "FACTORY_KUBE_CONTEXT is not set. Set it to the platform cluster's kubectl context, for example: FACTORY_KUBE_CONTEXT=\$(kubectl config current-context) make ..."
  kubectl config get-contexts -o name | grep -qxF "${FACTORY_KUBE_CONTEXT}" ||
    die "kubectl context '${FACTORY_KUBE_CONTEXT}' is not in the local kubeconfig"
}

kc() { kubectl --context "${FACTORY_KUBE_CONTEXT}" "$@"; }
kf() { kc --namespace "${FACTORY_NAMESPACE}" "$@"; }

# store_info sets STORE_BUCKET, STORE_PREFIX (s3 key prefix of the server,
# ending in /) and STORE_REGION from the chart's ObjectStore, so the scripts
# never carry their own copy of the bucket name.
store_info() {
  local json path
  json="$(kf get objectstores.barmancloud.cnpg.io factory-db-store -o json 2>/dev/null)" ||
    die "ObjectStore factory-db-store not found in ${FACTORY_NAMESPACE}: has Argo CD synced svc-factory yet?"
  path="$(jq -r '.spec.configuration.destinationPath' <<<"${json}")"
  STORE_REGION="$(jq -r '[.spec.instanceSidecarConfiguration.env[]? | select(.name == "AWS_REGION") | .value][0] // empty' <<<"${json}")"
  [[ "${path}" =~ ^s3://([^/]+)/?(.*)$ ]] || die "unexpected destinationPath ${path}"
  STORE_BUCKET="${BASH_REMATCH[1]}"
  STORE_PREFIX="${BASH_REMATCH[2]%/}"
  STORE_PREFIX="${STORE_PREFIX:+${STORE_PREFIX}/}factory-db/"
  [[ -n "${STORE_REGION}" ]] || die "ObjectStore has no AWS_REGION in instanceSidecarConfiguration.env"
}

s3() { aws --region "${STORE_REGION}" "$@"; }

# prefix_empty: true when the server prefix holds no objects at all.
prefix_empty() {
  local count
  # shellcheck disable=SC2016 # the backticks are a JMESPath literal.
  count="$(s3 s3api list-objects-v2 --bucket "${STORE_BUCKET}" --prefix "${STORE_PREFIX}" \
    --max-items 1 --query 'length(Contents || `[]`)' --output text)" || die "cannot list s3://${STORE_BUCKET}/${STORE_PREFIX}"
  [[ "${count}" == 0 ]]
}

# has_completed_backup: true when any base backup under the prefix is DONE.
has_completed_backup() {
  local key
  while read -r key; do
    [[ -z "${key}" || "${key}" == None ]] && continue
    if s3 s3 cp "s3://${STORE_BUCKET}/${key}" - 2>/dev/null | grep -qx 'status=DONE'; then
      return 0
    fi
  done < <(s3 s3api list-objects-v2 --bucket "${STORE_BUCKET}" --prefix "${STORE_PREFIX}base/" \
    --query "Contents[?ends_with(Key, 'backup.info')].Key" --output text | tr '\t' '\n')
  return 1
}

# wait_cluster_ready NAME TIMEOUT
wait_cluster_ready() {
  log "waiting up to $2 for Cluster $1 to be Ready"
  kf wait "clusters.postgresql.cnpg.io/$1" --for=condition=Ready --timeout="$2"
}

# primary_pod CLUSTER: name of the current primary instance pod.
primary_pod() {
  local pod
  pod="$(kf get pods -l "cnpg.io/cluster=$1,cnpg.io/instanceRole=primary" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [[ -n "${pod}" ]] || die "no primary pod for Cluster $1"
  printf '%s' "${pod}"
}

# psql_factory ARGS...: psql against database factory on the factory-db
# primary, over the pod's local socket. SQL is read from stdin.
psql_factory() {
  local pod
  pod="$(primary_pod factory-db)"
  kf exec -i "${pod}" -c postgres -- psql -X -q -v ON_ERROR_STOP=1 -d factory "$@"
}

# mint_token RUN TASK STAGE TTL: prints a gateway job token (never logged).
mint_token() {
  local token
  token="$(kf exec deploy/gateway -c gateway -- /gateway mint-token --run "$1" --task "$2" --stage "$3" --ttl "$4")" ||
    die "gateway mint-token failed"
  token="${token//[$'\r\n ']/}"
  [[ -n "${token}" ]] || die "gateway mint-token printed nothing"
  printf '%s' "${token}"
}

# runner_image: the runner image the chart derived for this deployment.
runner_image() {
  local image
  image="$(kf get configmap factory-images -o jsonpath='{.data.runner}')" || die "ConfigMap factory-images not found"
  [[ -n "${image}" ]] || die "ConfigMap factory-images has no runner image"
  printf '%s' "${image}"
}

# render TEMPLATE VAR...: replaces ${VAR} in TEMPLATE with the value of each
# named shell variable. Only the named variables are replaced.
render() {
  local template="$1" out var pattern value
  shift
  out="$(<"${template}")"
  for var in "$@"; do
    pattern="\${${var}}"
    value="${!var}"
    # Splice rather than ${out//pat/rep}: replacement quoting differs between
    # bash 3.2 (macOS) and 5.2+ (where an unquoted & is the match).
    while [[ "${out}" == *"${pattern}"* ]]; do
      out="${out%%"${pattern}"*}${value}${out#*"${pattern}"}"
    done
  done
  if [[ "${out}" =~ \$\{[A-Z_]+\} ]]; then
    die "unrendered placeholder ${BASH_REMATCH[0]} in ${template}"
  fi
  printf '%s\n' "${out}"
}

# create_token_secret NAMESPACE NAME TOKEN: Secret with exactly
# ANTHROPIC_AUTH_TOKEN. The token reaches kubectl on stdin: printf is a shell
# builtin, so it is never in any process's argv, and no temp file exists that
# an INT or TERM could leave behind (the earlier 0600 file needed a trap per
# signal to be removed reliably). --from-env-file=/dev/stdin was checked with
# kubectl v1.36 (create --dry-run=client).
create_token_secret() {
  printf 'ANTHROPIC_AUTH_TOKEN=%s\n' "$3" |
    kc --namespace "$1" create secret generic "$2" --from-env-file=/dev/stdin >/dev/null ||
    die "cannot create Secret $1/$2"
}

# revoke_token RUN: revokes every gateway token minted for RUN, for EXIT traps.
# Best-effort: it logs a failure and always returns 0, so it never masks the
# calling script's exit status (unrevoked tokens still expire at their TTL).
revoke_token() {
  local out
  if out="$(kf exec deploy/gateway -c gateway -- /gateway revoke-token --run "$1" 2>&1)"; then
    log "revoked gateway tokens for run $1: ${out//[$'\r\n']/ }"
  else
    log "WARNING: gateway revoke-token --run $1 failed; its tokens stay valid until their TTL: ${out//[$'\r\n']/ }"
  fi
  return 0
}

# run_job NAMESPACE JOB_NAME PURPOSE SECRET_NAME COMMAND_JSON [WAIT_FOR_POLICY]:
# applies the shared sandbox Job, waits for it to finish, prints its logs and
# returns 0 only if it succeeded. WAIT_FOR_POLICY (default 1) keeps the Job's
# wait-for-policy init container; 0 removes it, for the CONTROL=1 namespace,
# which has no NetworkPolicies, so the init container could only time out.
run_job() {
  # shellcheck disable=SC2034 # read by render through indirect expansion.
  local NAMESPACE="$1" JOB_NAME="$2" PURPOSE="$3" SECRET_NAME="$4" COMMAND="$5" IMAGE
  local wait_for_policy="${6:-1}"
  IMAGE="$(runner_image)"
  render "${REPO_ROOT}/deploy/sandbox/job.yaml.tmpl" JOB_NAME NAMESPACE IMAGE SECRET_NAME COMMAND PURPOSE |
    if [[ "${wait_for_policy}" == 0 ]]; then
      sed '/# begin wait-for-policy/,/# end wait-for-policy/d'
    else
      cat
    fi |
    kc apply -f - >/dev/null
  log "Job ${NAMESPACE}/${JOB_NAME} started (image ${IMAGE})"

  local deadline=$((SECONDS + 420)) succeeded failed
  while ((SECONDS < deadline)); do
    succeeded="$(kc --namespace "${NAMESPACE}" get job "${JOB_NAME}" -o jsonpath='{.status.succeeded}')"
    failed="$(kc --namespace "${NAMESPACE}" get job "${JOB_NAME}" -o jsonpath='{.status.failed}')"
    [[ "${succeeded:-0}" -gt 0 || "${failed:-0}" -gt 0 ]] && break
    sleep 5
  done

  log "logs of ${NAMESPACE}/${JOB_NAME}:"
  kc --namespace "${NAMESPACE}" logs "job/${JOB_NAME}" --all-containers --tail=-1 || true
  if [[ "${succeeded:-0}" -gt 0 ]]; then
    log "Job ${JOB_NAME} succeeded"
    return 0
  fi
  if [[ "${failed:-0}" -gt 0 ]]; then
    log "Job ${JOB_NAME} failed"
  else
    log "Job ${JOB_NAME} did not finish in time"
    kc --namespace "${NAMESPACE}" describe job "${JOB_NAME}" | tail -20 >&2 || true
  fi
  return 1
}
