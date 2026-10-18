#!/usr/bin/env bash
# make factory-status: is the factory up after `make up`? (plan Step 8, §7)
#
# - Empty backup prefix: first bring-up; prints "run make db-init", exits 0.
# - Otherwise waits for Cluster factory-db Ready and the gateway rolled out,
#   then prints the WAL archiver failed count and the last backup time.
# - Exits non-zero if the archiver failed count is above 0 or the gateway is
#   not Ready.
#
# Usage: FACTORY_KUBE_CONTEXT=<ctx> scripts/factory-status.sh
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TIMEOUT="${FACTORY_STATUS_TIMEOUT:-25m}"

require_context
need aws
store_info

if prefix_empty; then
  echo "first bring-up: run make db-init"
  echo "(s3://${STORE_BUCKET}/${STORE_PREFIX} is empty, so factory-db has nothing to recover from)"
  exit 0
fi

status=0

wait_cluster_ready factory-db "${TIMEOUT}" || die "Cluster factory-db is not Ready after ${TIMEOUT}"

log "waiting up to ${TIMEOUT} for the gateway"
if kf rollout status deploy/gateway --timeout="${TIMEOUT}" >/dev/null &&
  kf wait deploy/gateway --for=condition=Available --timeout=60s >/dev/null; then
  gateway="Ready"
else
  gateway="NOT Ready"
  status=1
fi

# Same source as the cnpg_pg_stat_archiver_failed_count metric. Read over psql
# because the exporter port is reachable only from the monitoring namespace.
archiver="$(psql_factory -A -t -F ' ' <<'SQL'
SELECT failed_count,
       coalesce(last_archived_wal, '-'),
       coalesce(to_char(last_archived_time AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '-'),
       coalesce(to_char(last_failed_time AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '-')
FROM pg_stat_archiver;
SQL
)" || die "cannot read pg_stat_archiver"
read -r failed_count last_wal last_archived last_failed <<<"${archiver}" || true

# The ObjectStore status survives a make down/up (it is rebuilt from the
# bucket); Backup objects from before the teardown do not.
last_backup="$(kf get objectstores.barmancloud.cnpg.io factory-db-store -o json |
  jq -r '.status.serverRecoveryWindow["factory-db"].lastSuccessfulBackupTime // empty')"
if [[ -z "${last_backup}" ]]; then
  last_backup="$(kf get backups.postgresql.cnpg.io -o json |
    jq -r '[.items[] | select(.spec.cluster.name == "factory-db" and .status.phase == "completed") | .status.stoppedAt] | max // empty')"
fi

cat <<EOF
factory-db:                          Ready
gateway:                             ${gateway}
cnpg_pg_stat_archiver_failed_count:  ${failed_count}
last archived WAL:                   ${last_wal} at ${last_archived}
last archive failure:                ${last_failed}
last successful backup:              ${last_backup:-none recorded}
store:                               s3://${STORE_BUCKET}/${STORE_PREFIX}
EOF

if [[ "${failed_count}" =~ ^[0-9]+$ ]] && ((failed_count > 0)); then
  log "WAL archiving has failed ${failed_count} time(s) since the last stats reset"
  status=1
elif [[ ! "${failed_count}" =~ ^[0-9]+$ ]]; then
  log "unexpected archiver failed count '${failed_count}'"
  status=1
fi
[[ "${gateway}" == Ready ]] || log "the gateway is not Ready"
exit "${status}"
