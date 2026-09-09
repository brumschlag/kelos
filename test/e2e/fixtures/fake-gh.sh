#!/bin/sh
set -eu

if [ "$#" -eq 12 ] && [ "$1" = "api" ] && [ "$2" = "--hostname" ] && [ "$3" = "github.com" ] && [ "$4" = "--method" ] && [ "$5" = "GET" ] && [ "$6" = "repos/kelos-dev/kelos/pulls" ] && [ "$7" = "-f" ] && [ "$8" = "state=all" ] && [ "$9" = "-f" ] && [ "${10}" = "head=kelos-dev:agent/session-status" ] && [ "${11}" = "-F" ] && [ "${12}" = "per_page=1" ]; then
  printf '%s\n' '[{"html_url":"https://github.com/kelos-dev/kelos/pull/42","state":"open","draft":false,"merged_at":null}]'
  exit 0
fi

if [ "$#" -eq 12 ] && [ "$1" = "api" ] && [ "$2" = "--hostname" ] && [ "$3" = "github.com" ] && [ "$4" = "graphql" ] && [ "$5" = "-f" ] && [ "$6" = "owner=kelos-dev" ] && [ "$7" = "-f" ] && [ "$8" = "repo=kelos" ] && [ "$9" = "-F" ] && [ "${10}" = "number=42" ] && [ "${11}" = "-f" ]; then
  case "${12}" in
    query=*commits*mergeQueueEntry*)
      printf '%s\n' '{"data":{"repository":{"pullRequest":{"url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""},{"__typename":"StatusContext","state":"SUCCESS"}]}}}}]},"mergeQueueEntry":null}}}}'
      exit 0
      ;;
  esac
fi

echo "Unsupported gh command: $*" >&2
exit 2
