#!/usr/bin/env bash
# make verify-row ID=<id> (plan Step 8, AC7): prints one agent_calls row from
# the factory-db primary. Run before make down and after make up; the two
# outputs must be identical.
#
# Usage: FACTORY_KUBE_CONTEXT=<ctx> scripts/verify-row.sh <id>
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

id="${1:-}"
[[ "${id}" =~ ^[0-9]+$ ]] || die "usage: verify-row.sh <numeric agent_calls id>"

require_context

out="$(psql_factory -x -v id="${id}" <<'SQL'
SELECT * FROM agent_calls WHERE id = :id;
SQL
)"
if [[ -z "${out}" || "${out}" == *"(0 rows)"* ]]; then
  die "no agent_calls row with id ${id}"
fi
printf '%s\n' "${out}"
