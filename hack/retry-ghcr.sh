#!/usr/bin/env bash

set -euo pipefail

retry_count="${GHCR_RETRY_COUNT:-0}"
delay="${GHCR_RETRY_INITIAL_DELAY_SECONDS:-60}"

if [[ ! "${retry_count}" =~ ^[0-9]+$ ]]; then
  echo "GHCR_RETRY_COUNT must be a non-negative integer" >&2
  exit 2
fi

if [[ ! "${delay}" =~ ^[0-9]+$ ]]; then
  echo "GHCR_RETRY_INITIAL_DELAY_SECONDS must be a non-negative integer" >&2
  exit 2
fi

if [[ $# -eq 0 ]]; then
  echo "Usage: retry-ghcr.sh <command> [args...]" >&2
  exit 2
fi

retries=0
output_file=""

cleanup() {
  if [[ -n "${output_file}" ]]; then
    rm -f "${output_file}"
  fi
}
trap cleanup EXIT

while true; do
  output_file="$(mktemp)"

  set +e
  "$@" 2>&1 | tee "${output_file}"
  status=${PIPESTATUS[0]}
  set -e

  if [[ ${status} -eq 0 ]]; then
    exit 0
  fi

  if [[ ${retries} -ge ${retry_count} ]] || ! grep -Fqi "secondary rate limit" "${output_file}"; then
    exit "${status}"
  fi

  rm -f "${output_file}"
  output_file=""

  echo "GHCR secondary rate limit reached, retrying in ${delay} seconds" >&2
  sleep "${delay}"

  retries=$((retries + 1))
  delay=$((delay * 2))
done
