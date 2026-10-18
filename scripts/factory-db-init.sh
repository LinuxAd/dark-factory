#!/usr/bin/env bash
# make db-init: first-ever bring-up of factory-db, once per account (plan
# Step 8, ADR 0001 Decision B-i).
#
# The chart's Cluster factory-db always bootstraps by recovery, so on the very
# first bring-up it has nothing to recover and stays stuck. This script:
#   1. refuses if s3://<bucket>/cnpg/factory-db/ is non-empty (with
#      --purge-failed it first clears a prefix that has no completed base
#      backup, for example after a failed earlier attempt);
#   2. applies the seed Cluster factory-db-seed (initdb, database and owner
#      factory, the chart's ObjectStore, serverName factory-db);
#   3. once the seed is Ready, takes a plugin Backup and waits for completed;
#   4. deletes Argo CD's stuck factory-db first and then the seed, so self-heal
#      recreates factory-db and its recovery finds the backup;
#   5. waits for factory-db to be Ready.
#
# Usage: FACTORY_KUBE_CONTEXT=<ctx> scripts/factory-db-init.sh [--purge-failed]
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PURGE_FAILED=false
for arg in "$@"; do
  case "${arg}" in
    --purge-failed) PURGE_FAILED=true ;;
    -h | --help) sed -n '2,18p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "unknown argument ${arg}" ;;
  esac
done

SEED_TIMEOUT="${DB_INIT_SEED_TIMEOUT:-15m}"
BACKUP_TIMEOUT_SECONDS="${DB_INIT_BACKUP_TIMEOUT_SECONDS:-1200}"
RECOVERY_TIMEOUT="${DB_INIT_RECOVERY_TIMEOUT:-30m}"

require_context
need aws
store_info
log "store: s3://${STORE_BUCKET}/${STORE_PREFIX} (${STORE_REGION})"

# 1. Only ever seed an empty prefix: two writers on one serverName is the one
#    thing B-i must never allow.
if ! prefix_empty; then
  if has_completed_backup; then
    die "s3://${STORE_BUCKET}/${STORE_PREFIX} already holds a completed base backup; factory-db recovers from it. Nothing to do (run make factory-status)."
  fi
  if [[ "${PURGE_FAILED}" != true ]]; then
    die "s3://${STORE_BUCKET}/${STORE_PREFIX} is not empty but has no completed base backup (a failed earlier attempt?). Re-run with PURGE_FAILED=1 to clear it."
  fi
  log "purging s3://${STORE_BUCKET}/${STORE_PREFIX} (no completed base backup)"
  s3 s3 rm --recursive --only-show-errors "s3://${STORE_BUCKET}/${STORE_PREFIX}"
  prefix_empty || die "prefix still not empty after purge"
fi

IMAGE_NAME="$(kf get clusters.postgresql.cnpg.io factory-db -o jsonpath='{.spec.imageName}' 2>/dev/null || true)"
[[ -n "${IMAGE_NAME}" ]] || die "Cluster factory-db not found (or has no imageName): has Argo CD synced svc-factory yet?"

# 2. Seed.
log "applying seed Cluster factory-db-seed (${IMAGE_NAME})"
render "${REPO_ROOT}/deploy/seed/factory-db-seed.yaml.tmpl" IMAGE_NAME | kf apply -f - >/dev/null
wait_cluster_ready factory-db-seed "${SEED_TIMEOUT}"

# 3. Base backup of the seed.
backup="factory-db-seed-initial-$(date -u +%Y%m%d%H%M%S)"
kf apply -f - >/dev/null <<EOF
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: ${backup}
  namespace: ${FACTORY_NAMESPACE}
spec:
  cluster:
    name: factory-db-seed
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
EOF
log "waiting up to ${BACKUP_TIMEOUT_SECONDS}s for Backup ${backup}"
deadline=$((SECONDS + BACKUP_TIMEOUT_SECONDS))
phase=""
while ((SECONDS < deadline)); do
  phase="$(kf get backups.postgresql.cnpg.io "${backup}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [[ "${phase}" == completed || "${phase}" == failed ]] && break
  sleep 10
done
if [[ "${phase}" != completed ]]; then
  kf get backups.postgresql.cnpg.io "${backup}" -o jsonpath='{.status.error}{"\n"}' >&2 || true
  die "Backup ${backup} ended in phase '${phase:-unknown}'. The seed is left running for inspection; delete it with: kubectl --context ${FACTORY_KUBE_CONTEXT} -n ${FACTORY_NAMESPACE} delete cluster factory-db-seed"
fi
log "Backup ${backup} completed"

# 4. Argo CD's Cluster first (keeps the two-writer window short, risk R2),
#    then the seed.
log "deleting the stuck Cluster factory-db (Argo CD self-heal recreates it)"
kf delete clusters.postgresql.cnpg.io factory-db --wait=true --timeout=5m
log "deleting the seed Cluster factory-db-seed"
kf delete clusters.postgresql.cnpg.io factory-db-seed --wait=true --timeout=5m

# 5. Wait for self-heal to recreate factory-db, then for its recovery.
log "waiting for Argo CD to recreate Cluster factory-db"
deadline=$((SECONDS + 600))
until kf get clusters.postgresql.cnpg.io factory-db >/dev/null 2>&1; do
  ((SECONDS < deadline)) || die "Cluster factory-db was not recreated within 10 minutes; check Application svc-factory in Argo CD"
  sleep 10
done
wait_cluster_ready factory-db "${RECOVERY_TIMEOUT}"
log "factory-db recovered from ${backup}. Next: make factory-status"
