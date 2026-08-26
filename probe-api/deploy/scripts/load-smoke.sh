#!/usr/bin/env bash

set -Eeuo pipefail

TARGET_URL="${LOAD_URL:-}"
REQUESTS="${LOAD_REQUESTS:-100}"
CONCURRENCY="${LOAD_CONCURRENCY:-10}"
EXPECTED_STATUS="${LOAD_EXPECTED_STATUS:-200}"
MAX_ERROR_RATE="${LOAD_MAX_ERROR_RATE:-1}"
MAX_P95_MS="${LOAD_MAX_P95_MS:-1000}"
CONNECT_TIMEOUT="${LOAD_CONNECT_TIMEOUT:-5}"
MAX_TIME="${LOAD_MAX_TIME:-15}"
CA_CERT="${LOAD_CA_CERT:-}"
INSECURE=0

usage() {
    cat <<'USAGE'
Usage:
  load-smoke.sh --url READ_ONLY_PANEL_URL [options]

The URL may also be supplied with LOAD_URL. It must use http(s) and contain
/api/v1/panel/ so this smoke test cannot target a write endpoint.

Options:
  --url URL                 Complete read-only panel API URL
  --requests N              Total requests (default: 100)
  --concurrency N           Maximum concurrent requests (default: 10)
  --expected-status CODE    Successful HTTP status (default: 200)
  --max-error-rate PCT      Maximum curl/status error percentage (default: 1)
  --max-p95-ms MS           Maximum p95 of successful requests (default: 1000)
  --connect-timeout SEC     curl connection timeout (default: 5)
  --max-time SEC            curl total timeout per request (default: 15)
  --cacert FILE             Trust this absolute PEM CA/certificate path
  --insecure                Allow an untrusted TLS certificate (preview only)
  -h, --help                Show this help

The process exits 1 when a threshold is exceeded or measurements are invalid,
and exits 2 for invalid arguments or missing prerequisites.
USAGE
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 2
}

is_positive_integer() {
    [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

is_nonnegative_number() {
    [[ "$1" =~ ^[0-9]+([.][0-9]+)?$ ]]
}

is_positive_number() {
    is_nonnegative_number "$1" &&
        awk -v value="$1" 'BEGIN { exit !(value > 0) }'
}

while (($# > 0)); do
    case "$1" in
        --url)
            (($# >= 2)) || die "--url requires a value"
            TARGET_URL="$2"
            shift 2
            ;;
        --requests)
            (($# >= 2)) || die "--requests requires a value"
            REQUESTS="$2"
            shift 2
            ;;
        --concurrency)
            (($# >= 2)) || die "--concurrency requires a value"
            CONCURRENCY="$2"
            shift 2
            ;;
        --expected-status)
            (($# >= 2)) || die "--expected-status requires a value"
            EXPECTED_STATUS="$2"
            shift 2
            ;;
        --max-error-rate)
            (($# >= 2)) || die "--max-error-rate requires a value"
            MAX_ERROR_RATE="$2"
            shift 2
            ;;
        --max-p95-ms)
            (($# >= 2)) || die "--max-p95-ms requires a value"
            MAX_P95_MS="$2"
            shift 2
            ;;
        --connect-timeout)
            (($# >= 2)) || die "--connect-timeout requires a value"
            CONNECT_TIMEOUT="$2"
            shift 2
            ;;
        --max-time)
            (($# >= 2)) || die "--max-time requires a value"
            MAX_TIME="$2"
            shift 2
            ;;
        --cacert)
            (($# >= 2)) || die "--cacert requires a value"
            CA_CERT="$2"
            shift 2
            ;;
        --insecure)
            INSECURE=1
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            die "unknown option: $1"
            ;;
    esac
done

for dependency in curl awk mktemp sort sed tail cat date; do
    command -v "$dependency" >/dev/null 2>&1 || die "$dependency is required"
done

[[ -n "$TARGET_URL" ]] || die "--url or LOAD_URL is required"
[[ ! "$TARGET_URL" =~ [[:space:]] ]] || die "URL must not contain whitespace"
case "$TARGET_URL" in
    http://* | https://*) ;;
    *) die "URL must start with http:// or https://" ;;
esac
case "$TARGET_URL" in
    *'@'* | *'#'*) die "URL credentials and fragments are not allowed" ;;
esac
[[ "$TARGET_URL" == *'/api/v1/panel/'* ]] ||
    die "URL must target a read-only /api/v1/panel/ endpoint"

is_positive_integer "$REQUESTS" || die "requests must be a positive integer"
is_positive_integer "$CONCURRENCY" || die "concurrency must be a positive integer"
((REQUESTS <= 100000)) || die "requests must not exceed 100000"
((CONCURRENCY <= 1000)) || die "concurrency must not exceed 1000"
((CONCURRENCY <= REQUESTS)) || die "concurrency must not exceed requests"
[[ "$EXPECTED_STATUS" =~ ^[1-5][0-9][0-9]$ ]] ||
    die "expected status must be an HTTP status from 100 to 599"
is_nonnegative_number "$MAX_ERROR_RATE" || die "max error rate must be a number from 0 to 100"
awk -v value="$MAX_ERROR_RATE" 'BEGIN { exit !(value <= 100) }' ||
    die "max error rate must be a number from 0 to 100"
is_positive_number "$MAX_P95_MS" || die "max p95 must be greater than zero"
is_positive_number "$CONNECT_TIMEOUT" || die "connect timeout must be greater than zero"
is_positive_number "$MAX_TIME" || die "max time must be greater than zero"
if [[ -n "$CA_CERT" ]]; then
    [[ "$CA_CERT" == /* && -f "$CA_CERT" && ! -L "$CA_CERT" && -r "$CA_CERT" ]] ||
        die "CA certificate must be a readable absolute regular file, not a symlink"
fi
((INSECURE == 0)) || [[ -z "$CA_CERT" ]] || die "--cacert and --insecure are mutually exclusive"

TMP_DIR="$(mktemp -d -t probe-load-smoke.XXXXXX)"
trap 'rm -rf -- "$TMP_DIR"' EXIT
RESULTS_FILE="$TMP_DIR/results.tsv"
LATENCIES_FILE="$TMP_DIR/latencies-ms.txt"
SORTED_LATENCIES_FILE="$TMP_DIR/latencies-ms.sorted.txt"

CURL_ARGS=(
    --disable
    --silent
    --request GET
    --output /dev/null
    --connect-timeout "$CONNECT_TIMEOUT"
    --max-time "$MAX_TIME"
    --header 'Accept: application/json'
    --header 'Cookie:'
    --header 'Authorization:'
    --header 'X-CSRF-Token:'
    --user-agent 'probe-load-smoke/1'
    --write-out $'%{http_code}\t%{time_total}'
)
if ((INSECURE == 1)); then
    CURL_ARGS+=(--insecure)
elif [[ -n "$CA_CERT" ]]; then
    CURL_ARGS+=(--cacert "$CA_CERT")
fi

run_request() {
    local request_number="$1"
    local output curl_rc http_code elapsed

    if output="$(curl "${CURL_ARGS[@]}" --url "$TARGET_URL" \
        2>"$TMP_DIR/${request_number}.stderr")"; then
        curl_rc=0
    else
        curl_rc=$?
    fi
    IFS=$'\t' read -r http_code elapsed <<<"$output"
    http_code="${http_code:-000}"
    elapsed="${elapsed:-0}"
    printf '%s\t%s\t%s\n' "$curl_rc" "$http_code" "$elapsed" \
        >"$TMP_DIR/${request_number}.result"
}

printf 'Probe Panel read-only load smoke\n'
printf '  url: %s\n' "$TARGET_URL"
printf '  requests: %s\n  concurrency: %s\n' "$REQUESTS" "$CONCURRENCY"
printf '  expected status: %s\n' "$EXPECTED_STATUS"
printf '  thresholds: error_rate<=%s%% p95<=%sms\n\n' \
    "$MAX_ERROR_RATE" "$MAX_P95_MS"

started_epoch="$(date +%s)"
active=0
request_pid_head=0
request_pid_tail=0
request_pids=()

wait_for_oldest_request() {
    local oldest_pid="${request_pids[request_pid_head]}"

    wait "$oldest_pid" || true
    unset "request_pids[request_pid_head]"
    request_pid_head=$((request_pid_head + 1))
    active=$((active - 1))
}

for ((request_number = 1; request_number <= REQUESTS; request_number++)); do
    run_request "$request_number" &
    request_pids[request_pid_tail]="$!"
    request_pid_tail=$((request_pid_tail + 1))
    active=$((active + 1))
    if ((active >= CONCURRENCY)); then
        wait_for_oldest_request
    fi
done
while ((active > 0)); do
    wait_for_oldest_request
done
finished_epoch="$(date +%s)"

: >"$RESULTS_FILE"
for ((request_number = 1; request_number <= REQUESTS; request_number++)); do
    result_file="$TMP_DIR/${request_number}.result"
    if [[ -s "$result_file" ]]; then
        cat -- "$result_file" >>"$RESULTS_FILE"
    else
        printf '99\t000\t0\n' >>"$RESULTS_FILE"
    fi
done

: >"$LATENCIES_FILE"
summary="$(awk -F '\t' -v expected="$EXPECTED_STATUS" -v latency_file="$LATENCIES_FILE" '
    BEGIN { total=0; success=0; curl_fail=0; status_fail=0; invalid=0 }
    {
        total++
        if ($1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ || length($2) != 3 || $3 !~ /^[0-9]+([.][0-9]+)?$/) {
            invalid++
            next
        }
        if (($1 + 0) != 0) {
            curl_fail++
        } else if ($2 != expected) {
            status_fail++
        } else {
            success++
            printf "%.3f\n", ($3 * 1000) >> latency_file
        }
    }
    END { printf "%d %d %d %d %d\n", total, success, curl_fail, status_fail, invalid }
' "$RESULTS_FILE")"
read -r measured_total success_count curl_fail_count status_fail_count invalid_count <<<"$summary"
error_count=$((curl_fail_count + status_fail_count + invalid_count))
error_rate="$(awk -v errors="$error_count" -v total="$measured_total" \
    'BEGIN { if (total == 0) print "100.000"; else printf "%.3f", (errors * 100 / total) }')"

min_ms="n/a"
avg_ms="n/a"
p95_ms="n/a"
max_ms="n/a"
if ((success_count > 0)); then
    sort -n "$LATENCIES_FILE" >"$SORTED_LATENCIES_FILE"
    p95_rank=$(((success_count * 95 + 99) / 100))
    min_ms="$(sed -n '1p' "$SORTED_LATENCIES_FILE")"
    p95_ms="$(sed -n "${p95_rank}p" "$SORTED_LATENCIES_FILE")"
    max_ms="$(tail -n 1 "$SORTED_LATENCIES_FILE")"
    avg_ms="$(awk '{ sum += $1 } END { if (NR > 0) printf "%.3f", sum / NR }' \
        "$SORTED_LATENCIES_FILE")"
fi

duration_seconds=$((finished_epoch - started_epoch))
printf 'Results\n'
printf '  duration_seconds: %d\n' "$duration_seconds"
printf '  measured: %d\n  successful: %d\n' "$measured_total" "$success_count"
printf '  curl_failures: %d\n  unexpected_statuses: %d\n  invalid_measurements: %d\n' \
    "$curl_fail_count" "$status_fail_count" "$invalid_count"
printf '  error_rate_percent: %s\n' "$error_rate"
printf '  latency_ms_successful: min=%s avg=%s p95=%s max=%s\n' \
    "$min_ms" "$avg_ms" "$p95_ms" "$max_ms"
printf '  http_status_counts:'
awk -F '\t' '$1 == 0 { count[$2]++ } END { for (code in count) printf " %s=%d", code, count[code] }' \
    "$RESULTS_FILE"
printf '\n'

threshold_failed=0
if ((measured_total != REQUESTS)) || ((invalid_count > 0)); then
    printf 'FAIL: measurement set is incomplete or invalid\n' >&2
    threshold_failed=1
fi
if ! awk -v actual="$error_rate" -v maximum="$MAX_ERROR_RATE" \
    'BEGIN { exit !(actual <= maximum) }'; then
    printf 'FAIL: error rate %s%% exceeds %s%%\n' "$error_rate" "$MAX_ERROR_RATE" >&2
    threshold_failed=1
fi
if ((success_count == 0)); then
    printf 'FAIL: no successful request is available for latency evaluation\n' >&2
    threshold_failed=1
elif ! awk -v actual="$p95_ms" -v maximum="$MAX_P95_MS" \
    'BEGIN { exit !(actual <= maximum) }'; then
    printf 'FAIL: p95 %sms exceeds %sms\n' "$p95_ms" "$MAX_P95_MS" >&2
    threshold_failed=1
fi

if ((threshold_failed > 0)); then
    exit 1
fi
printf 'PASS: all configured thresholds were met\n'
