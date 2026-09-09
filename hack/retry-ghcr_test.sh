#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
retry_script="${repo_root}/hack/retry-ghcr.sh"

if [[ "${1:-}" == "fake-command" ]]; then
  count=0
  if [[ -f "${FAKE_COUNTER_FILE}" ]]; then
    count="$(<"${FAKE_COUNTER_FILE}")"
  fi
  count=$((count + 1))
  printf '%s\n' "${count}" >"${FAKE_COUNTER_FILE}"

  if [[ ${count} -le ${FAKE_FAILURES_BEFORE_SUCCESS} ]]; then
    echo "${FAKE_FAILURE_MESSAGE}" >&2
    exit "${FAKE_FAILURE_STATUS}"
  fi
  exit 0
fi

test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

fail() {
  echo "Test failed: $*" >&2
  exit 1
}

assert_attempts() {
  local counter_file="$1"
  local expected="$2"
  local actual
  actual="$(<"${counter_file}")"
  [[ "${actual}" == "${expected}" ]] || fail "expected ${expected} attempts, got ${actual}"
}

counter_file="${test_dir}/immediate-success-count"
FAKE_COUNTER_FILE="${counter_file}" \
  FAKE_FAILURES_BEFORE_SUCCESS=0 \
  FAKE_FAILURE_MESSAGE="" \
  FAKE_FAILURE_STATUS=23 \
  GHCR_RETRY_COUNT=3 \
  GHCR_RETRY_INITIAL_DELAY_SECONDS=0 \
  "${retry_script}" "$0" fake-command >/dev/null 2>&1
assert_attempts "${counter_file}" 1

counter_file="${test_dir}/eventual-success-count"
FAKE_COUNTER_FILE="${counter_file}" \
  FAKE_FAILURES_BEFORE_SUCCESS=3 \
  FAKE_FAILURE_MESSAGE="You have exceeded a secondary rate limit" \
  FAKE_FAILURE_STATUS=23 \
  GHCR_RETRY_COUNT=3 \
  GHCR_RETRY_INITIAL_DELAY_SECONDS=0 \
  "${retry_script}" "$0" fake-command >/dev/null 2>&1
assert_attempts "${counter_file}" 4

counter_file="${test_dir}/permanent-failure-count"
set +e
FAKE_COUNTER_FILE="${counter_file}" \
  FAKE_FAILURES_BEFORE_SUCCESS=2 \
  FAKE_FAILURE_MESSAGE="Build failed" \
  FAKE_FAILURE_STATUS=23 \
  GHCR_RETRY_COUNT=3 \
  GHCR_RETRY_INITIAL_DELAY_SECONDS=0 \
  "${retry_script}" "$0" fake-command >/dev/null 2>&1
status=$?
set -e
[[ ${status} -eq 23 ]] || fail "expected status 23, got ${status}"
assert_attempts "${counter_file}" 1

counter_file="${test_dir}/exhausted-count"
set +e
FAKE_COUNTER_FILE="${counter_file}" \
  FAKE_FAILURES_BEFORE_SUCCESS=4 \
  FAKE_FAILURE_MESSAGE="You have exceeded a secondary rate limit" \
  FAKE_FAILURE_STATUS=23 \
  GHCR_RETRY_COUNT=3 \
  GHCR_RETRY_INITIAL_DELAY_SECONDS=0 \
  "${retry_script}" "$0" fake-command >/dev/null 2>&1
status=$?
set -e
[[ ${status} -eq 23 ]] || fail "expected status 23, got ${status}"
assert_attempts "${counter_file}" 4

echo "GHCR retry tests passed"
