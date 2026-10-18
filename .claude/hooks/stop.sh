#!/usr/bin/env bash
# Stop hook: run the Go tests before Claude finishes, if Go code changed
# (SPEC.md section 10). Skips when Docker or the orchestrator Makefile is
# absent, and on the second stop attempt so it cannot loop. Exit 2 makes
# Claude carry on and fix the failure.
set -uo pipefail

root="${CLAUDE_PROJECT_DIR:-$(pwd)}"
input="$(cat)"
if command -v jq >/dev/null 2>&1 && [[ "$(jq -r '.stop_hook_active // false' <<<"${input}" 2>/dev/null)" == true ]]; then
  exit 0
fi
[[ -f "${root}/orchestrator/Makefile" ]] || exit 0
command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 || exit 0
# Only when Go code or migrations differ from HEAD.
git -C "${root}" status --porcelain -- '*.go' 'go.mod' 'go.sum' 'memory/migrations' 2>/dev/null | grep -q . || exit 0

if ! out="$(make -s -C "${root}/orchestrator" test 2>&1)"; then
  printf 'hook: make -C orchestrator test failed:\n%s\n' "${out}" | tail -80 >&2
  exit 2
fi
exit 0
