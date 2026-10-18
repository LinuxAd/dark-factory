#!/usr/bin/env bash
# Sandbox isolation probe (plan Step 6, AC4). Run inside a sandbox Job by
# `make sandbox-probe`. Prints one PASS/FAIL line per check and exits non-zero
# if any check fails. Every network check sets the proxy explicitly, so the
# result never depends on how curl reads proxy variables.
#
# In the CONTROL=1 run (a namespace without NetworkPolicies) this probe is
# expected to fail: that shows the policies, not something else, provide the
# isolation.
set -euo pipefail

PROXY="${HTTPS_PROXY:-${https_proxy:-}}"
GATEWAY="${ANTHROPIC_BASE_URL:-}"
TOKEN="${ANTHROPIC_AUTH_TOKEN:-}"
DB_HOST="${PROBE_DB_HOST:-factory-db-rw.factory.svc.cluster.local}"
DB_PORT="${PROBE_DB_PORT:-5432}"

failures=0

pass() { printf 'PASS %-22s %s\n' "$1" "$2"; }
fail() { printf 'FAIL %-22s %s\n' "$1" "$2"; failures=$((failures + 1)); }

# check NAME DESCRIPTION EXPECT CMD...: EXPECT is "ok" (command must succeed)
# or "blocked" (command must fail).
check() {
  local name="$1" desc="$2" expect="$3"
  shift 3
  if "$@" >/dev/null 2>&1; then
    if [[ "${expect}" == ok ]]; then pass "${name}" "${desc}"; else fail "${name}" "${desc} (reachable)"; fi
  else
    if [[ "${expect}" == blocked ]]; then pass "${name}" "${desc}"; else fail "${name}" "${desc} (failed)"; fi
  fi
}

direct() { curl --noproxy '*' --silent --show-error --connect-timeout 5 --max-time 8 --output /dev/null "$@"; }
via_proxy() { curl --proxy "${PROXY}" --silent --show-error --fail --connect-timeout 5 --max-time 15 --output /dev/null "$@"; }
# shellcheck disable=SC2016 # $1/$2 expand in the child shell, by design.
tcp_open() { timeout 5 bash -c 'exec 3<>"/dev/tcp/$1/$2"' _ "$1" "$2"; }

# No settle sleep (dropped, not kept as belt-and-braces): the Job's
# wait-for-policy init container only lets this container start once direct
# egress is blocked, so a sleep here would add nothing. In the CONTROL=1 run the
# init container is removed and there are no policies to wait for.

echo "probe: pod $(hostname), $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 1. No Anthropic key anywhere the pod can see. The image layers are covered
#    by CI, which never sees the key.
if env | grep -q 'sk-ant'; then
  fail key-in-env "an environment variable contains sk-ant"
else
  pass key-in-env "no environment variable contains sk-ant"
fi

scan_paths=("${HOME:-/home/runner}" /tmp)
# Every mount except the root filesystem and the kernel pseudo-filesystems.
while read -r _ mountpoint fstype _; do
  case "${mountpoint}" in
    / | /proc | /proc/* | /sys | /sys/* | /dev | /dev/*) continue ;;
  esac
  case "${fstype}" in
    proc | sysfs | cgroup | cgroup2 | devpts | mqueue) continue ;;
  esac
  [[ " ${scan_paths[*]} " == *" ${mountpoint} "* ]] || scan_paths+=("${mountpoint}")
done </proc/mounts
if hits="$(grep -rIls 'sk-ant' "${scan_paths[@]}" 2>/dev/null)" && [[ -n "${hits}" ]]; then
  fail key-in-files "sk-ant found in: ${hits//$'\n'/ }"
else
  pass key-in-files "no sk-ant in ${scan_paths[*]}"
fi

# 2. No AWS or Kubernetes credentials.
if env | grep -Eq '^AWS_'; then
  fail aws-env "AWS_* variables present: $(env | grep -Eo '^AWS_[A-Z_]+' | tr '\n' ' ')"
else
  pass aws-env "no AWS_* (Pod Identity) variables"
fi
if [[ -e /var/run/secrets/pods.eks.amazonaws.com || -e /var/run/secrets/eks.amazonaws.com ]]; then
  fail aws-token-file "an EKS identity token is mounted"
else
  pass aws-token-file "no EKS identity token mounted"
fi
if [[ -e /var/run/secrets/kubernetes.io/serviceaccount/token ]]; then
  fail k8s-token "a Kubernetes service account token is mounted"
else
  pass k8s-token "no Kubernetes service account token mounted"
fi

# 3. Network isolation.
check imds "instance metadata 169.254.169.254 unreachable" blocked direct http://169.254.169.254/latest/meta-data/
check pod-identity-agent "Pod Identity agent 169.254.170.23 unreachable" blocked direct http://169.254.170.23/
check postgres "${DB_HOST}:${DB_PORT} unreachable" blocked tcp_open "${DB_HOST}" "${DB_PORT}"
check direct-anthropic "https://api.anthropic.com unreachable without the proxy" blocked direct https://api.anthropic.com/
check direct-internet "https://proxy.golang.org unreachable without the proxy" blocked direct https://proxy.golang.org/

# 4. The proxy allows the allowlist and nothing else.
if [[ -z "${PROXY}" ]]; then
  fail proxy-config "HTTPS_PROXY is not set"
else
  check proxy-allowed "proxy.golang.org reachable through the proxy" ok via_proxy https://proxy.golang.org/golang.org/x/text/@v/list
  check proxy-denied "example.com refused by the proxy" blocked via_proxy https://example.com/
  check proxy-anthropic "api.anthropic.com refused by the proxy" blocked via_proxy https://api.anthropic.com/
fi

# 5. The gateway answers with the Job's token and refuses without one.
if [[ -z "${GATEWAY}" || -z "${TOKEN}" ]]; then
  fail gateway-config "ANTHROPIC_BASE_URL or ANTHROPIC_AUTH_TOKEN is not set"
else
  check gateway-token "${GATEWAY}/v1/models with the Job token" ok \
    curl --noproxy '*' --silent --show-error --fail --max-time 15 --output /dev/null \
    -H "Authorization: Bearer ${TOKEN}" -H 'anthropic-version: 2023-06-01' "${GATEWAY}/v1/models"
  code="$(curl --noproxy '*' --silent --max-time 15 --output /dev/null --write-out '%{http_code}' \
    -H 'anthropic-version: 2023-06-01' "${GATEWAY}/v1/models" || true)"
  if [[ "${code}" == 401 ]]; then
    pass gateway-no-token "${GATEWAY}/v1/models without a token returns 401"
  else
    fail gateway-no-token "${GATEWAY}/v1/models without a token returned ${code:-no response}"
  fi
fi

if ((failures > 0)); then
  echo "probe: ${failures} check(s) FAILED"
  exit 1
fi
echo "probe: all checks passed"
