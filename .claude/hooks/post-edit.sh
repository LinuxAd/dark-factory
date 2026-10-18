#!/usr/bin/env bash
# PostToolUse hook (Edit/Write/MultiEdit): lint the file Claude just changed
# (SPEC.md section 10). Fast and quiet: it does nothing when the tool it needs
# is absent. Exit 2 sends the failure back to Claude to fix.
#   *.go  -> make -C orchestrator lint  (golangci-lint in Docker)
#   *.sh  -> shellcheck
#   *.tf  -> terraform validate in that directory, if it has been initialised
set -uo pipefail

command -v jq >/dev/null 2>&1 || exit 0
root="${CLAUDE_PROJECT_DIR:-$(pwd)}"
file="$(jq -r '.tool_input.file_path // empty' 2>/dev/null)"
[[ -n "${file}" && -f "${file}" ]] || exit 0

run() {
  local out
  if ! out="$("$@" 2>&1)"; then
    printf 'hook: %s failed:\n%s\n' "$*" "${out}" | tail -60 >&2
    exit 2
  fi
}

case "${file}" in
  *.go)
    [[ -f "${root}/orchestrator/Makefile" ]] || exit 0
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 || exit 0
    run make -s -C "${root}/orchestrator" lint
    ;;
  *.sh)
    command -v shellcheck >/dev/null 2>&1 || exit 0
    run shellcheck -x -P SCRIPTDIR "${file}"
    ;;
  *.tf)
    command -v terraform >/dev/null 2>&1 || exit 0
    dir="$(dirname "${file}")"
    [[ -d "${dir}/.terraform" ]] || exit 0
    run terraform -chdir="${dir}" validate -no-color
    ;;
esac
exit 0
