#!/bin/sh
# This file intentionally searches for literal shell variables in source text.
# shellcheck disable=SC2016

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
LOAD_SMOKE=$ROOT_DIR/probe-api/deploy/scripts/load-smoke.sh

fail() {
    printf '%s\n' "load smoke contract: $*" >&2
    exit 1
}

assert_contains() {
    needle=$1
    grep -Fq -- "$needle" "$LOAD_SMOKE" ||
        fail "missing required contract: $needle"
}

[ -f "$LOAD_SMOKE" ] || fail "missing load smoke script: $LOAD_SMOKE"
bash -n "$LOAD_SMOKE"

if grep -Eq '(^|[[:space:]])wait[[:space:]]+-n([[:space:]]|$)' "$LOAD_SMOKE"; then
    fail 'wait -n is unavailable on the supported CentOS 7 Bash 4.2 runtime'
fi

# The queue has explicit monotonic head/tail indexes so a concurrency of one
# cannot reuse an emptied array index while the head continues to advance.
assert_contains 'request_pid_head=0'
assert_contains 'request_pid_tail=0'
assert_contains 'request_pids=()'
assert_contains 'wait_for_oldest_request()'
assert_contains 'local oldest_pid="${request_pids[request_pid_head]}"'
assert_contains 'wait "$oldest_pid" || true'
assert_contains 'unset "request_pids[request_pid_head]"'
assert_contains 'request_pid_head=$((request_pid_head + 1))'
assert_contains 'request_pids[request_pid_tail]="$!"'
assert_contains 'request_pid_tail=$((request_pid_tail + 1))'
assert_contains 'if ((active >= CONCURRENCY)); then'
assert_contains 'while ((active > 0)); do'

[ "$(grep -Ec '^[[:space:]]+wait_for_oldest_request$' "$LOAD_SMOKE")" -eq 2 ] ||
    fail 'the concurrency gate and final drain must both consume the PID queue'

# Failed or missing workers must still be measured and handled by the existing
# threshold exit path instead of causing set -e to abort during wait.
assert_contains "printf '99\\t000\\t0\\n' >>\"\$RESULTS_FILE\""
assert_contains 'error_count=$((curl_fail_count + status_fail_count + invalid_count))'
assert_contains 'if ((threshold_failed > 0)); then'
assert_contains '    exit 1'

printf '%s\n' 'load smoke contract: PASS'
